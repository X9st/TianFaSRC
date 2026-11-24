import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "天伐 · 自主安全验证平台",
  description: "在明确授权范围内，把规划、执行、证据与复测组织成可恢复的数据闭环。",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}

