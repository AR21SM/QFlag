import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "QFlag",
  description: "Fault-tolerant feature flags backed by a custom Raft KV store.",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
