// The design system (../../design), as it is: its tokens and components, and the scripts that give the icons, the
// logo, the illustrations and the motion. They set globals, declared here.
import '../../design/tokens.css';
import '../../design/components.css';
import '../../design/icons.js';
import '../../design/motion.js';
import '../../design/illos.js';

type Illo = (facts?: { name?: string; mail?: string; domain?: string; made?: boolean }) => string;

declare global {
  interface Window {
    I: (name: string, cls?: string) => string;
    MARK: string;
    LOCKUP: string;
    ILLO: Record<string, Illo>;
    HL: {
      still: () => boolean;
      run: (update: () => void, opts?: { type?: 'push' | 'pop' | 'fade' }) => Promise<void>;
    };
  }
}

/** The markup of one of the system's icons. */
export const icon = (name: string, cls = 'icon'): string => window.I(name, cls);
