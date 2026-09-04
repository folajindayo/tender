import path from "node:path";

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Locally there are unrelated lockfiles above this directory, so the trace
  // root is pinned to the monorepo to stop builds picking them up. On Vercel
  // only apps/web is uploaded, so there is nothing above to confuse -- and
  // pointing the root outside the deployment makes Next resolve its manifests
  // against a path that does not exist there.
  ...(process.env.VERCEL
    ? {}
    : { outputFileTracingRoot: path.join(import.meta.dirname, "../../") }),
  // The camera needs a secure context, so this is always served over HTTPS in
  // any environment a phone will actually use.
  async headers() {
    return [
      {
        source: "/sw.js",
        headers: [
          { key: "Cache-Control", value: "no-cache, no-store, must-revalidate" },
          { key: "Service-Worker-Allowed", value: "/" },
        ],
      },
    ];
  },
};

export default nextConfig;
