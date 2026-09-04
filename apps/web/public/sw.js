// Minimal shell cache.
//
// Money state is never cached: every API response must come from the network,
// because showing a stale balance during a handover would be worse than
// showing nothing at all.

const SHELL = "tender-shell-v1";
const ASSETS = ["/", "/manifest.json", "/icon.svg"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(SHELL).then((cache) => cache.addAll(ASSETS)).then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET") return;

  const url = new URL(request.url);
  // Anything that is not this origin's static shell goes straight to the network.
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/v1/")) return;

  event.respondWith(
    fetch(request)
      .then((res) => {
        if (res.ok && request.mode === "navigate") {
          const copy = res.clone();
          caches.open(SHELL).then((cache) => cache.put(request, copy));
        }
        return res;
      })
      .catch(() => caches.match(request).then((hit) => hit ?? caches.match("/"))),
  );
});
