import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "天罚 · 自主漏洞挖掘平台",
  description: "在明确授权范围内，自主探索资产与漏洞线索，保留可复核的证据、复测与报告。",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}

