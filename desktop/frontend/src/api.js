// Every call into the Go side goes through this object. main.js fills it from
// the generated wailsjs bindings at startup; the tests fill it with fakes. Code
// elsewhere never imports wailsjs directly, so a module can be loaded under
// node without a window.go.
export const api = {};

export function configure(bindings) {
  for (const key of Object.keys(api)) delete api[key];
  Object.assign(api, bindings);
}
