import type { Metadata, Viewport } from "next";
import { OrgProvider } from "@/components/org";
import { Providers } from "@/components/providers";
import { Shell } from "@/components/shell";
import "./globals.css";

export const metadata: Metadata = {
  title: "CueFlow",
  description: "Self-hosted social agent ops console",
  icons: { icon: "/favicon.svg" },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="antialiased">
        <Providers>
          <OrgProvider>
            <Shell>{children}</Shell>
          </OrgProvider>
        </Providers>
      </body>
    </html>
  );
}
