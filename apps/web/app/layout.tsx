import type { Metadata, Viewport } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Tender — send cash you are holding",
  description:
    "Move physical naira to anyone, instantly, without a bank. Your cash goes to somebody nearby who wanted notes; the value goes where you sent it.",
  manifest: "/manifest.json",
  appleWebApp: { capable: true, statusBarStyle: "black-translucent", title: "Tender" },
};

export const viewport: Viewport = {
  themeColor: "#0a0c0b",
  width: "device-width",
  initialScale: 1,
  // The capture screen must not shift under the user's thumb.
  maximumScale: 1,
  viewportFit: "cover",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        {children}
        <script
          dangerouslySetInnerHTML={{
            __html: `
              if ('serviceWorker' in navigator) {
                window.addEventListener('load', function () {
                  navigator.serviceWorker.register('/sw.js').catch(function () {});
                });
              }
            `,
          }}
        />
      </body>
    </html>
  );
}
