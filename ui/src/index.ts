// The bundle's entry. The app imports this file and calls what it default
// exports, handing it the one object a module receives.
//
// `defineModule` is an identity function. It exists so an author gets the
// host's types at this call site without importing the host.
import { defineModule } from '@prusa/connect-ui/slots';

import Bindings from './bindings.svelte';
import { holdHost } from './host';
import './styles.css';

export default defineModule((host) => {
  // The screen reads the host and nothing else, and it is mounted after this
  // call, so the host is held rather than threaded down.
  holdHost(host);

  // Held to `contributes` in module.yaml: a name the manifest does not declare
  // is refused at run time and the app drops this module.
  //
  // Settings, Extensions is the module's own detail page, and this tab sits on
  // it beside the nine settings the manifest declares, which is the point: the
  // bindings list is the one setting a text area cannot express.
  host.register.settings('bindings', Bindings);
});
