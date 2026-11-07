import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { type Plugin, defineConfig } from 'vite';

// The demo build (--mode demo, see .env.demo and src/demo) runs the real UI
// on a static host: a runtime config for a control plane (its one Site is
// simulated too, so Synthetics and the Site picker show). `npm run build:demo`
// also copies index.html to 404.html so GitHub Pages serves the app on any path.
const RUNTIME_CONFIG_JS = `window.__RUNTIME_CONFIG__ = ${JSON.stringify({ MODE: 'control-plane' })};\n`;

function demoStaticFiles(): Plugin {
  return {
    name: 'obzly-demo-static-files',
    configureServer(server) {
      server.middlewares.use('/runtime-config.js', (_req, res) => {
        res.setHeader('Content-Type', 'text/javascript');
        res.end(RUNTIME_CONFIG_JS);
      });
    },
    generateBundle() {
      this.emitFile({
        type: 'asset',
        fileName: 'runtime-config.js',
        source: RUNTIME_CONFIG_JS,
      });
    },
  };
}

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const demo = mode === 'demo';
  return {
    plugins: [react(), tailwindcss(), ...(demo ? [demoStaticFiles()] : [])],
    // A literal, so production builds drop the demo code entirely.
    define: { __OBZLY_DEMO__: JSON.stringify(demo) },
  };
});
