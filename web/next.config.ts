import type { NextConfig } from "next";

const config: NextConfig = {
  // A self-contained server, so the Docker image needs no node_modules.
  output: "standalone",
  poweredByHeader: false,
};

export default config;
