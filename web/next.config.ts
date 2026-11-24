import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV === "development";

const nextConfig: NextConfig = {
  output: isDev ? undefined : "export",
  images: { unoptimized: true },
  poweredByHeader: false,
  agentRules: false,
  allowedDevOrigins: ["127.0.0.1"],
  turbopack: { root: process.cwd() },
  ...(isDev ? {
    async rewrites() {
      const apiOrigin = process.env.TIANFA_DEV_API_ORIGIN ?? "http://127.0.0.1:8080";
      return [{ source: "/health/:path*", destination: `${apiOrigin}/health/:path*` }];
    },
  } : {}),
};

export default nextConfig;

