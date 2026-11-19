# Getting mDNS into a container

**The problem in one paragraph.** A container on a Docker bridge is on its own layer-2 network. mDNS is
multicast to `224.0.0.251` on UDP 5353, and a stock Docker host does not carry that group between the
bridge and your LAN, in either direction. So a container can reach a light by address
(`curl http://10.0.0.136/json/info` answers every time) while a browse for `_wled._tcp.local` returns
nothing at all. This document is the host-side change that fixes the browse, without giving the container
the host's network namespace.

It is written from a Prusa Connect module in Docker on a CentOS Stream 9 host, but nothing here is
specific to that module: any container that needs to see mDNS (WLED lights, printers, Chromecasts,
HomeKit) needs the same thing.

## What it takes: one setting on the host, and a restart of avahi

The host already runs `avahi-daemon` to publish its own `.local` name. Avahi has a reflector mode that
forwards mDNS packets between the interfaces it serves. That is the whole change:

```ini
# /etc/avahi/avahi-daemon.conf
[reflector]
enable-reflector=yes
```

Before you change anything, write down what `.local` resolution looks like, so that you can tell a
regression from a coincidence. From another machine on the LAN, with the host's name in place of `kevin`:

```bash
getent hosts kevin.local
# 10.0.0.160      kevin.local
```

That answer must be the same after the change. On this host it was, for `kevin.local`, for two lights'
own names, and for the mDNS browse of `_wled._tcp.local` from the LAN.

Then restart avahi so it reads the file — **not** the container, and not Docker:

```bash
sudo systemctl restart avahi-daemon
systemctl is-active avahi-daemon      # active
```

Nothing else. No interface names, no `br-…` id, no compose change, no container restart.

## Why this works, and why nothing else is needed

**The reflector re-originates the traffic rather than routing it.** Avahi receives a packet on one
interface, then sends it out of every other interface it serves. Both directions are packets the host
itself emits, so they cross the bridge exactly as any other multicast frame from the host does. That is
why this works where kernel forwarding does not.

**No interface is named, so a recreated bridge still works.** Avahi serves every interface it finds and
picks up new ones while running. On the host this was measured with a bridge that did not exist when
avahi started:

```bash
docker network create --subnet 172.30.0.0/16 mdns-probe-net
docker run --rm --network mdns-probe-net … /probe -svc _wled._tcp.local
# RESULT: 19 instances of _wled._tcp.local
docker network rm mdns-probe-net
```

and avahi logged it joining and leaving that interface by name on its own. If you re-create the network
Docker gives the module, its bridge gets a new `br-<id>` and the reflector follows it with no edit.

**`net.ipv4.conf.*.mc_forwarding` is not needed, and on some kernels cannot be set at all.** Kernel IP
multicast forwarding is a different mechanism: it needs a multicast routing daemon (`pimd`, `smcroute`)
to install forwarding entries, and on this host the sysctl is registered read-only — see failure mode 1.
Because the reflector re-originates packets in user space, none of that matters here. If you are on a
kernel where the knob is writable, setting it does no harm and no good for this path.

**You do not need `network_mode: host`.** That was the previous workaround for a browse, and it costs
your published ports. The reflector gets the same result while the module keeps its own bridge address.

## How to check it worked

`avahi-browse` is the obvious tool and it does not work inside a bare container: it needs an
`avahi-daemon` running in that container to talk to. Use a raw query instead. This is a complete,
dependency-free browser — save it as `probe.go`:

```go
// probe.go — a dependency-free mDNS/DNS-SD browser for checking that multicast
// reaches a container. Build with CGO_ENABLED=0 and run it on the module's network.
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

func enc(n string) []byte {
	var b []byte
	for _, p := range strings.Split(strings.TrimSuffix(n, "."), ".") {
		if p == "" {
			continue
		}
		b = append(b, byte(len(p)))
		b = append(b, p...)
	}
	return append(b, 0)
}

// name reads a DNS name at off, following compression pointers.
func name(m []byte, off int) (string, int, bool) {
	var l []string
	ret, jumped := off, false
	for i := 0; i < 128; i++ {
		if off >= len(m) {
			return "", 0, false
		}
		c := int(m[off])
		if c == 0 {
			off++
			if !jumped {
				ret = off
			}
			return strings.Join(l, "."), ret, true
		}
		if c&0xc0 == 0xc0 {
			if off+1 >= len(m) {
				return "", 0, false
			}
			if !jumped {
				ret = off + 2
			}
			jumped = true
			off = int(binary.BigEndian.Uint16(m[off:]) & 0x3fff)
			continue
		}
		off++
		if off+c > len(m) {
			return "", 0, false
		}
		l = append(l, string(m[off:off+c]))
		off += c
	}
	return "", 0, false
}

func main() {
	svc := "_wled._tcp.local"
	if len(os.Args) > 1 {
		svc = os.Args[1]
	}
	var ifi *net.Interface
	ifs, _ := net.Interfaces()
	for i := range ifs {
		if addrs, _ := ifs[i].Addrs(); len(addrs) > 0 && ifs[i].Flags&net.FlagLoopback == 0 {
			ifi = &ifs[i]
			break
		}
	}
	conn, err := net.ListenMulticastUDP("udp4", ifi, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
	if err != nil {
		panic(err)
	}
	dst := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	ptr := map[string]bool{}
	srv := map[string]string{}
	addrs := map[string][]string{}
	buf := make([]byte, 9000)

	ask := func(q string, t uint16) {
		m := make([]byte, 12)
		binary.BigEndian.PutUint16(m[4:], 1) // qdcount
		m = append(m, enc(q)...)
		m = append(m, 0, byte(t), 0, 1) // unicast-response bit clear
		conn.WriteToUDP(m, dst)
	}
	listen := func(d time.Duration) {
		end := time.Now().Add(d)
		for {
			conn.SetReadDeadline(end)
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			m := buf[:n]
			if len(m) < 12 {
				continue
			}
			off := 12
			for i := 0; i < int(binary.BigEndian.Uint16(m[4:])); i++ {
				_, o, ok := name(m, off)
				if !ok {
					break
				}
				off = o + 4
			}
			total := int(binary.BigEndian.Uint16(m[6:])) + int(binary.BigEndian.Uint16(m[8:]))
			for i := 0; i < total; i++ {
				nm, o, ok := name(m, off)
				if !ok || o+10 > len(m) {
					break
				}
				t := binary.BigEndian.Uint16(m[o:])
				rl := int(binary.BigEndian.Uint16(m[o+8:]))
				if o+10+rl > len(m) {
					break
				}
				rd := m[o+10 : o+10+rl]
				switch {
				case t == 12: // PTR
					if tg, _, ok := name(m, o+10); ok {
						ptr[tg] = true
					}
				case t == 33 && rl > 6: // SRV
					if tg, _, ok := name(m, o+16); ok {
						srv[nm] = fmt.Sprintf("%s:%d", tg, binary.BigEndian.Uint16(rd[4:]))
					}
				case t == 1 && len(rd) == 4: // A
					addrs[strings.ToLower(nm)] = append(addrs[strings.ToLower(nm)], net.IP(rd).String())
				}
				off = o + 10 + rl
			}
		}
	}

	ask(svc, 12) // 1: PTR
	listen(3 * time.Second)
	var inst []string
	for i := range ptr {
		if strings.HasSuffix(strings.ToLower(i), strings.ToLower("."+svc)) {
			inst = append(inst, i)
		}
	}
	sort.Strings(inst)
	for _, i := range inst {
		ask(i, 33) // 2: SRV …
		ask(i, 16) // … and TXT
	}
	listen(2 * time.Second)
	hosts := map[string]bool{}
	for _, i := range inst {
		if s, ok := srv[i]; ok {
			hosts[strings.Split(s, ":")[0]] = true
		}
	}
	var hs []string
	for h := range hosts {
		hs = append(hs, h)
	}
	sort.Strings(hs)
	for _, h := range hs {
		ask(h, 1) // 3: A
	}
	listen(2 * time.Second)

	for _, i := range inst {
		host := ""
		if s, ok := srv[i]; ok {
			host = s
		}
		var uniq []string
		seen := map[string]bool{}
		for _, a := range addrs[strings.ToLower(strings.Split(host, ":")[0])] {
			if !seen[a] {
				seen[a] = true
				uniq = append(uniq, a)
			}
		}
		fmt.Printf("%-28s %-34s %v\n", strings.TrimSuffix(i, "."+svc), host, uniq)
	}
	fmt.Printf("RESULT: %d instances of %s\n", len(inst), svc)
}
```

Run it on the module's own network — no privileges, no host networking. Build it once in a Go image and
run the binary in any image you already have:

```bash
docker run --rm -v "$PWD":/src -w /src -e GOCACHE=/tmp/cache golang:1.26 \
  sh -c 'CGO_ENABLED=0 go build -o probe probe.go'
docker run --rm --network prusa_connect_backend -v "$PWD/probe":/probe:ro alpine:3 /probe
```

A good answer looks like this — every light by name, with the host and port it advertises and the
address behind it (19 here, trimmed):

```
CyberDesk                    CyberDesk.local:80                 [10.0.0.253]
Predator-Meditate            Predator-Meditate.local:80         [10.0.0.250]
damon-award                  damon-award.local:80               [10.0.0.99]
morpheus                     morpheus.local:80                  [10.0.0.136]
RESULT: 19 instances of _wled._tcp.local
```

Before the change the same command printed `RESULT: 0 instances`. If you are checking from another
machine on the LAN instead, `avahi-browse -rt _wled._tcp` is fine — there the daemon is running.

## Failure modes worth knowing

**1. `mc_forwarding` may be read-only, and chasing it wastes an afternoon.** On the host it is
`-r--r--r--` on every interface and a write is refused even as root with every capability:

```
$ ls -l /proc/sys/net/ipv4/conf/all/mc_forwarding
-r--r--r--. 1 root root 0 Nov 19 10:35 /proc/sys/net/ipv4/conf/all/mc_forwarding
$ echo 1 > /proc/sys/net/ipv4/conf/all/mc_forwarding
Permission denied
# and from uid 0 with every capability (CapEff=0x1ffffffffff), in the host's own network
# namespace, the write is refused the same way, and the daemon's route in is closed too:
setsockopt(MRT_INIT, 32) -> err=operation not supported
```

The knob is the kernel's "a multicast routing daemon is attached" flag, and this kernel refuses to
attach one. It does not matter: the reflector does not use it. Do not conclude from this that mDNS
cannot cross a bridge — it is the wrong knob.

**2. A browse that returns names but no addresses is not half working — it is one query short.** The
reflector answers from the host's own cache, and a cached PTR answer carries only PTR records. A single
`_wled._tcp.local` PTR query therefore gives you the instance names and nothing to connect to. A real
DNS-SD browse is three queries in sequence: PTR for `_wled._tcp.local`, then SRV and TXT for each
`<instance>._wled._tcp.local`, then A/AAAA for each host. Do all three or you will conclude the reflector
is broken.

**3. The setting is read at start.** `systemctl restart avahi-daemon` — a reload does not pick up
`enable-reflector`.

**4. The reflector reflects onto every interface avahi serves**, including a VPN interface if you have
one: your LAN's mDNS becomes visible there, and each packet is duplicated onto every bridge on the host.
For a home LAN that is a few packets a second, and for a Docker host with many networks it is still
small. If you do not want it, name the interfaces — at the price of having to edit the file again when
Docker re-creates the bridge:

```ini
[server]
deny-interfaces=zt12345678
```

**5. Restarting avahi briefly interrupts `.local` resolution for everything else on the host.** Seconds.
If the host publishes a name your other machines rely on, that is the moment they may notice; nothing is
lost.

**6. A host with no avahi at all** (one running only `systemd-resolved`) has nothing to reflect. The
equivalent is a one-purpose repeater, `mdns-repeater`, which forwards 224.0.0.251 between two named
interfaces. Run it in a container sharing the host's network namespace, because it must see both the LAN
interface and the bridge:

```bash
docker run -d --name mdns-repeater --network host --restart unless-stopped \
  <image with mdns-repeater> mdns-repeater eth0 br-<id> enp4s0
```

That path was not needed here and is **untested on this host**; it is written down because it is the
answer when avahi is not an option. It also names interfaces, so it needs editing when the bridge
changes.

## How to undo it

One command, if you kept the copy this change made:

```bash
sudo cp -a /etc/avahi/avahi-daemon.conf.pre-mdns /etc/avahi/avahi-daemon.conf && sudo systemctl restart avahi-daemon
```

Without the backup, delete the `enable-reflector=yes` line and restart avahi. Either way, the check is
the same browse: it goes back to `RESULT: 0 instances`, and `.local` resolution is unaffected in both
directions. That revert was run here and re-applied: 19 instances, 0 instances, 19 instances.

## What the module needs, if you are writing the Go side

The container-side work is a browser, not a setting. In order:

1. **Join and send.** One UDP socket on `0.0.0.0:5353` with `SO_REUSEADDR`, joined to `224.0.0.251` on
   the container's own interface, TTL 255, one question per packet with the unicast-response bit clear.
2. **Three stages, in sequence** — PTR, then SRV/TXT per instance, then A/AAAA per host (failure mode 2).
   Budget about 3 s for the first and 1–2 s for each of the others; a browse that returns nothing inside
   6 s is not going to.
3. **Parse with compression pointers.** Names in mDNS answers are compressed (`0xC0` prefix); a parser
   that reads labels straight through will work on some packets and silently mis-read others.
4. **Treat the `mac` TXT record as identity.** WLED publishes `mac=` in TXT and it survives a DHCP lease;
   the instance name is whatever somebody typed into the WLED UI, so two lights can share it.
5. **Never block a request on it.** A browse is a listen window; a page that waits for it is a page that
   hangs when a light is asleep. Cache the result and answer from the cache.
6. **Expect no answer when the host has not done the above** — and say so on screen, rather than showing
   an empty list as if the network were empty.
