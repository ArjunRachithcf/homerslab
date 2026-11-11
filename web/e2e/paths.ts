// Where the flow tests find the checkout's files: the binary, the test sysroot and the design gallery. The checkout can
// sit anywhere, under a folder with a space or non-ASCII letters in its name.
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));

/** The real binary (`go build -o bin/homerslab ./cmd/homerslab`), or the one HOMERSLAB_BIN names. */
export const bin = process.env.HOMERSLAB_BIN ?? path.join(here, '..', '..', 'bin', 'homerslab');
export const sysroot = path.join(here, '..', '..', 'testdata', 'primary');
export const galleryUrl = pathToFileURL(path.join(here, '..', '..', 'design', 'index.html')).href;
