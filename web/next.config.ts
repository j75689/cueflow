import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV === "development";
const apiTarget = process.env.CUEFLOW_API ?? "http://127.0.0.1:3000";

const config: NextConfig = {
  // Production builds are a static export embedded into the Go binary.
  output: isDev ? undefined : "export",
  trailingSlash: true,
  images: { unoptimized: true },
  ...(isDev
    ? {
        // API paths never carry a trailing slash; don't let Next redirect them.
        skipTrailingSlashRedirect: true,
        async rewrites() {
          return [{ source: "/api/:path*", destination: `${apiTarget}/api/:path*` }];
        },
      }
    : {}),
};

export default config;
