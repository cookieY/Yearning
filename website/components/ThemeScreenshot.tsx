import { useEffect, useState } from 'react';
import { useDark, useLang, withBase } from '@rspress/core/runtime';

interface ThemeScreenshotProps {
  name: 'dashboard' | 'precheck' | 'detail' | 'list' | 'query' | 'admin';
  captionZh: string;
  captionEn?: string;
}

export function ThemeScreenshot({ name, captionZh, captionEn }: ThemeScreenshotProps) {
  const isDark = useDark();
  const lang = useLang();
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
  }, []);

  const isEn = lang.startsWith('en');
  const loc = isEn ? 'en-US' : 'zh-CN';
  const theme = mounted ? (isDark ? 'dark' : 'light') : 'dark';
  const src = withBase(`/screenshots/${name}-${loc}-${theme}.png`);
  const caption = isEn && captionEn ? captionEn : captionZh;

  return (
    <figure className="yn-doc-shot">
      <img src={src} alt={caption} width={1440} height={900} loading="lazy" decoding="async" />
      <figcaption>{caption}</figcaption>
    </figure>
  );
}
