module github.com/robchartier/wled-printer-status

go 1.26.11

require github.com/prusa3d/prusa-connect/sdk v0.0.0

// The sdk is built from the checkout this module was written beside, so a
// change there is seen here without a release.
//
// The path is relative on purpose. A container-absolute path such as
// /work/prusa-connect/sdk resolves only under the mount a build happens to
// use, so the tree does not build for a reader who opens it on the host or in
// an editor. ../prusa-connect/sdk is the same directory from either place: the
// checkout sits beside this module, and both sit under /work when a container
// mounts their parent.
replace github.com/prusa3d/prusa-connect/sdk => ../prusa-connect/sdk
