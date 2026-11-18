# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.1.0 - unreleased

### Added

- `GET /wled-printer-status` answers one line and the principal the host authorized.
- The setting `message` sets the line that route answers with.
- The install asks for no permission. Reading one printer and following its event stream will need
  `observe`, but the code that does that is not part of anything the module serves yet, so the grant
  is withheld rather than requested and left unused.
