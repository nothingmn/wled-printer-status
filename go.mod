module github.com/robchartier/wled-printer-status

go 1.26.11

require github.com/prusa3d/prusa-connect/sdk v0.0.0

// The sdk is not published yet, so a build takes it from a checkout of Prusa
// Connect. The marketplace builder makes that checkout itself, at .core/ in
// this tree, and the path below is the path it makes. A sibling path such as
// ../prusa-connect/sdk is a path that exists on the machine the module was
// written on and nowhere a reviewer or CI runs, so the build there finds
// nothing to replace.
//
// A local build makes .core a relative symlink to ../prusa-connect, which
// resolves on the host and under the container mount alike. .core is not
// committed. README.md has the one command that creates it.
replace github.com/prusa3d/prusa-connect/sdk => ./.core/sdk
