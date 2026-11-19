import { copyFileSync, cpSync, mkdirSync, rmSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { moduleBuild, moduleDev } from '@prusa/connect-ui/build';

// The bundle the host serves. `moduleBuild()` makes the choices every module
// makes -- one ES entry with a fixed name, the app's import map kept bare,
// runes, and the module's Tailwind folded in -- so the only thing written here
// is where the files go and what travels beside them.
//
// The bundle root is `dist/`: the release layout `prusa-connect module add`
// reads is module.yaml at the root and the entry under `ui/`, which is what
// module.yaml's `ui.entry: ui/index.js` names. The marketplace builder copies
// `ui/dist/ui` into `release/ui`, so the two spellings have to agree.
const BUNDLE_ROOT = fileURLToPath(new URL('./dist/', import.meta.url));
const MANIFEST = fileURLToPath(new URL('../module.yaml', import.meta.url));
const LOCALES = fileURLToPath(new URL('./src/locales/', import.meta.url));

/**
 * Puts the module's own manifest at the root of the bundle, which is where the
 * host reads the ui block from, and the catalogues beside the entry, which is
 * where it reads those. The digest covers the whole directory, so the manifest
 * the host reads is the one the install recorded.
 *
 * The directory is cleared first rather than written over: the digest counts
 * every file in it, so a file left behind by an earlier build would be part of
 * what the install records and what the host serves.
 */
const bundleLayout = () => ({
  name: 'wled-printer-status:bundle-layout',
  buildStart: {
    order: 'pre' as const,
    handler: () => {
      rmSync(BUNDLE_ROOT, { recursive: true, force: true });
      mkdirSync(BUNDLE_ROOT, { recursive: true });
    },
  },
  closeBundle: {
    order: 'post' as const,
    handler: () => {
      copyFileSync(MANIFEST, `${BUNDLE_ROOT}module.yaml`);
      cpSync(LOCALES, `${BUNDLE_ROOT}ui/locales`, { recursive: true });
    },
  },
});

const bundle = moduleBuild({ entry: 'src/index.ts', outDir: 'dist/ui' });

// `pnpm dev` develops the module behind its own dev server, in front of the
// core PCL_CORE_URL names. `pnpm build` writes the release.
export default ({ command }: { command: 'build' | 'serve' }) =>
  command === 'serve'
    ? moduleDev()
    : { ...bundle, plugins: [...(bundle.plugins ?? []), bundleLayout()] };
