// The bundle's entry. The app imports this file and calls what it default
// exports, handing it the one object a module receives.
//
// `defineModule` is an identity function. It exists so an author gets the
// host's types at this call site without importing the host.
import { defineModule } from '@prusa/connect-ui/slots';

import { holdHost } from './host';
import Shell from './shell.svelte';
import './styles.css';

export default defineModule((host) => {
  // The screens read the host and nothing else, and they are mounted after this
  // call, so the host is held rather than threaded down.
  holdHost(host);

  // One registration, for the module's one tab. Held to `contributes` in
  // module.yaml: a name the manifest does not declare is refused at run time and
  // the app drops this module, so the id here and the id there have to be the
  // same string -- which is why it still says `bindings`, the name this tab had
  // when the bindings list was all it held.
  //
  // Settings, Extensions is the module's own detail page. docs/product-requirements.md
  // §2 puts eight screens behind this one tab, and §3 decides they navigate inside
  // it rather than as eight tabs of the host's: the shell draws the rail, and each
  // screen is a view inside it. The host gives a module a tab, not a router, so
  // adding a screen is one entry in `screens.ts` and nothing here.
  host.register.settings('bindings', Shell);
});
