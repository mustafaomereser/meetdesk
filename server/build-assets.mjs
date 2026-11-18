// İzleme sayfasını (web/) buluşma sunucusuna paketler: src/assets.js üretilir.
// wrangler deploy / dev bunu [build] adımında kendisi çalıştırır.
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const web = join(here, "..", "web");
const files = {
  "view.html": "text/html; charset=utf-8",
  "view.js": "text/javascript; charset=utf-8",
  "rtc.js": "text/javascript; charset=utf-8",
  "titlebar.js": "text/javascript; charset=utf-8",
  "style.css": "text/css; charset=utf-8",
  "icon.png": "image/png",
};
const out = {};
for (const [name, type] of Object.entries(files)) {
  const buf = readFileSync(join(web, name));
  out[name] = { type, b64: type.startsWith("image/"), body: type.startsWith("image/") ? buf.toString("base64") : buf.toString("utf8") };
}
writeFileSync(join(here, "src", "assets.js"), "// Otomatik üretilir (build-assets.mjs). Düzenlemeyin.\nexport const ASSETS = " + JSON.stringify(out) + ";\n");
console.log("izleme sayfası paketlendi:", Object.keys(out).join(", "));
