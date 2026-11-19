import { useEffect, useRef } from 'react';
import { useDark } from '@rspress/core/runtime';

interface ConduitPulse {
  lane: number;
  offset: number;
  speed: number;
  length: number;
  branch: 'default' | 'san' | 'gate';
}

const PULSES: ConduitPulse[] = [
  { lane: 0, offset: 0.04, speed: 0.16, length: 0.16, branch: 'default' },
  { lane: 1, offset: 0.36, speed: 0.14, length: 0.14, branch: 'san' },
  { lane: 2, offset: 0.68, speed: 0.18, length: 0.18, branch: 'gate' },
  { lane: 3, offset: 0.21, speed: 0.13, length: 0.13, branch: 'default' },
  { lane: 4, offset: 0.54, speed: 0.15, length: 0.15, branch: 'gate' },
  { lane: 5, offset: 0.82, speed: 0.17, length: 0.16, branch: 'san' },
];

function isoProject(
  x: number,
  y: number,
  z: number,
  originX: number,
  originY: number,
  scale: number,
): [number, number] {
  const cos30 = 0.8660254;
  const sin30 = 0.5;
  return [
    originX + (x - y) * cos30 * scale,
    originY + (x + y) * sin30 * scale - z * scale,
  ];
}

export function HeroIsometricCanvas() {
  const isDark = useDark();
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const darkRef = useRef(isDark);

  useEffect(() => {
    darkRef.current = isDark;
  }, [isDark]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const finePointer = window.matchMedia('(hover: hover) and (pointer: fine)').matches;

    let rafId = 0;
    let isVisible = true;
    let time = 4.6;
    let pointerX = 0;
    let pointerY = 0;
    let targetX = 0;
    let targetY = 0;

    const drawScene = (t: number) => {
      const ctx = canvas.getContext('2d');
      if (!ctx) return;

      const dpr = Math.min(2, window.devicePixelRatio || 1);
      const rect = canvas.getBoundingClientRect();
      const cssW = Math.max(1, rect.width);
      const cssH = Math.max(1, rect.height);
      const w = Math.round(cssW * dpr);
      const h = Math.round(cssH * dpr);

      if (canvas.width !== w || canvas.height !== h) {
        canvas.width = w;
        canvas.height = h;
      }

      ctx.clearRect(0, 0, w, h);

      const dark = darkRef.current;
      const isCompact = cssW < 900;
      const parallaxX = pointerX * 18 * dpr;
      const parallaxY = pointerY * 12 * dpr;
      const cx = (isCompact ? cssW * 0.52 : cssW * 0.72) * dpr + parallaxX;
      const cy = (isCompact ? cssH * 0.54 : cssH * 0.48) * dpr + parallaxY;

      const gridCell = Math.max(36, Math.min(58, cssW * 0.04)) * dpr;
      const cos30 = 0.8660254;
      const sin30 = 0.5;
      const maxDist = Math.max(w, h) * 0.68;
      const cols = Math.ceil(w / (gridCell * cos30)) + 4;
      const rows = Math.ceil(h / (gridCell * sin30)) + 4;

      // 1. Gaussian-enveloped isometric lattice grid
      ctx.lineWidth = Math.max(1, dpr * 0.85);
      for (let i = -rows; i <= cols + rows; i++) {
        const midDist = Math.abs(i * gridCell * 0.65);
        const env = Math.exp(-(midDist * midDist) / (2 * (w * 0.34) * (w * 0.34)));
        if (env < 0.035) continue;

        const baseAlpha = (dark ? 0.14 : 0.11) * env;
        ctx.strokeStyle = dark
          ? `rgba(186, 198, 232, ${baseAlpha.toFixed(3)})`
          : `rgba(24, 28, 48, ${baseAlpha.toFixed(3)})`;

        ctx.beginPath();
        ctx.moveTo(cx + i * gridCell * cos30 - maxDist * cos30, cy + i * gridCell * sin30 - maxDist * sin30);
        ctx.lineTo(cx + i * gridCell * cos30 + maxDist * cos30, cy + i * gridCell * sin30 + maxDist * sin30);
        ctx.stroke();

        ctx.beginPath();
        ctx.moveTo(cx + i * gridCell * cos30 - maxDist * cos30, cy - i * gridCell * sin30 + maxDist * sin30);
        ctx.lineTo(cx + i * gridCell * cos30 + maxDist * cos30, cy - i * gridCell * sin30 - maxDist * sin30);
        ctx.stroke();
      }

      // 2. Focused crimson optical halo behind central governance prism
      const auraRadius = Math.min(w, h) * (isCompact ? 0.42 : 0.38);
      const aura = ctx.createRadialGradient(cx, cy, auraRadius * 0.04, cx, cy, auraRadius);
      if (dark) {
        aura.addColorStop(0, 'rgba(225, 29, 116, 0.22)');
        aura.addColorStop(0.42, 'rgba(225, 29, 116, 0.07)');
        aura.addColorStop(1, 'rgba(7, 8, 13, 0)');
      } else {
        aura.addColorStop(0, 'rgba(190, 18, 93, 0.14)');
        aura.addColorStop(0.45, 'rgba(190, 18, 93, 0.04)');
        aura.addColorStop(1, 'rgba(244, 245, 250, 0)');
      }
      ctx.fillStyle = aura;
      ctx.beginPath();
      ctx.arc(cx, cy, auraRadius, 0, Math.PI * 2);
      ctx.fill();

      // 3. Six isometric SQL governance optical conduits
      const laneRadius = Math.min(w, h) * (isCompact ? 0.56 : 0.5);
      const angles = [
        Math.PI / 6,
        (5 * Math.PI) / 6,
        (7 * Math.PI) / 6,
        (11 * Math.PI) / 6,
        Math.PI / 2,
        (3 * Math.PI) / 2,
      ];

      angles.forEach((angle, idx) => {
        const x1 = cx + Math.cos(angle) * laneRadius;
        const y1 = cy + Math.sin(angle) * laneRadius;
        const x0 = cx + Math.cos(angle) * (gridCell * 1.15);
        const y0 = cy + Math.sin(angle) * (gridCell * 1.15);

        ctx.strokeStyle = dark ? 'rgba(225, 29, 116, 0.16)' : 'rgba(190, 18, 93, 0.18)';
        ctx.lineWidth = 1.1 * dpr;
        ctx.beginPath();
        ctx.moveTo(x0, y0);
        ctx.lineTo(x1, y1);
        ctx.stroke();

        const nodeX = cx + Math.cos(angle) * (laneRadius * 0.62);
        const nodeY = cy + Math.sin(angle) * (laneRadius * 0.62);
        ctx.fillStyle = dark ? 'rgba(238, 240, 246, 0.35)' : 'rgba(18, 20, 30, 0.3)';
        ctx.beginPath();
        ctx.arc(nodeX, nodeY, 3.2 * dpr, 0, Math.PI * 2);
        ctx.fill();

        const p = PULSES[idx];
        const phase = (t * p.speed + p.offset) % 1;
        const inward = idx % 2 === 0;
        const u0 = inward ? 1 - phase : phase;
        const u1 = inward ? Math.max(0, u0 - p.length) : Math.min(1, u0 + p.length);

        const px0 = x0 + (x1 - x0) * u0;
        const py0 = y0 + (y1 - y0) * u0;
        const px1 = x0 + (x1 - x0) * u1;
        const py1 = y0 + (y1 - y0) * u1;

        const beam = ctx.createLinearGradient(px0, py0, px1, py1);
        const pulseRgb =
          p.branch === 'gate'
            ? dark
              ? '52, 211, 153'
              : '5, 150, 105'
            : dark
              ? '244, 63, 142'
              : '190, 18, 93';

        beam.addColorStop(0, `rgba(${pulseRgb}, 0)`);
        beam.addColorStop(0.55, `rgba(${pulseRgb}, 0.9)`);
        beam.addColorStop(1, `rgba(${pulseRgb}, 0.15)`);

        ctx.strokeStyle = beam;
        ctx.lineWidth = 2.4 * dpr;
        ctx.beginPath();
        ctx.moveTo(px0, py0);
        ctx.lineTo(px1, py1);
        ctx.stroke();

        ctx.fillStyle = `rgba(${pulseRgb}, 0.96)`;
        ctx.beginPath();
        ctx.arc(px1, py1, 2.6 * dpr, 0, Math.PI * 2);
        ctx.fill();
      });

      // 4. Concentric telemetry calibration ring
      const ringR = Math.min(w, h) * (isCompact ? 0.21 : 0.22);
      ctx.save();
      ctx.translate(cx, cy);
      ctx.rotate(t * 0.08);
      ctx.strokeStyle = dark ? 'rgba(226, 232, 248, 0.14)' : 'rgba(18, 20, 30, 0.13)';
      ctx.lineWidth = 1 * dpr;
      ctx.setLineDash([4 * dpr, 8 * dpr]);
      ctx.beginPath();
      ctx.arc(0, 0, ringR, 0, Math.PI * 2);
      ctx.stroke();
      ctx.setLineDash([]);

      ctx.strokeStyle = dark ? 'rgba(244, 63, 142, 0.48)' : 'rgba(190, 18, 93, 0.45)';
      ctx.lineWidth = 1.6 * dpr;
      for (let q = 0; q < 4; q++) {
        const a = (q * Math.PI) / 2;
        ctx.beginPath();
        ctx.arc(0, 0, ringR * 1.14, a - 0.12, a + 0.12);
        ctx.stroke();
      }
      ctx.restore();

      // 5. Central 3D-projected Yearning Isometric Cube Prism (outer governance shell + inner AST core)
      const breath = Math.sin(t * 0.9) * 0.08;
      const outerScale = Math.min(w, h) * (isCompact ? 0.125 : 0.135);
      const innerScale = outerScale * (0.56 + breath * 0.18);
      const rotZ = t * 0.16;

      const drawWireCube = (
        scale: number,
        zOffset: number,
        strokeStyle: string,
        fillFace: string,
        lineWidth: number,
      ) => {
        const pts: [number, number, number][] = [
          [-1, -1, -1],
          [1, -1, -1],
          [1, 1, -1],
          [-1, 1, -1],
          [-1, -1, 1],
          [1, -1, 1],
          [1, 1, 1],
          [-1, 1, 1],
        ];
        const c = Math.cos(rotZ);
        const s = Math.sin(rotZ);
        const projected = pts.map(([px, py, pz]) =>
          isoProject(px * c - py * s, px * s + py * c, pz + zOffset, cx, cy, scale),
        );

        ctx.fillStyle = fillFace;
        ctx.beginPath();
        ctx.moveTo(projected[4][0], projected[4][1]);
        ctx.lineTo(projected[5][0], projected[5][1]);
        ctx.lineTo(projected[6][0], projected[6][1]);
        ctx.lineTo(projected[7][0], projected[7][1]);
        ctx.closePath();
        ctx.fill();

        const edges: [number, number][] = [
          [0, 1], [1, 2], [2, 3], [3, 0],
          [4, 5], [5, 6], [6, 7], [7, 4],
          [0, 4], [1, 5], [2, 6], [3, 7],
        ];

        ctx.strokeStyle = strokeStyle;
        ctx.lineWidth = lineWidth;
        ctx.lineJoin = 'round';
        ctx.beginPath();
        for (const [a, b] of edges) {
          ctx.moveTo(projected[a][0], projected[a][1]);
          ctx.lineTo(projected[b][0], projected[b][1]);
        }
        ctx.stroke();

        ctx.fillStyle = strokeStyle;
        for (const [vx, vy] of projected) {
          ctx.beginPath();
          ctx.arc(vx, vy, 2.2 * dpr, 0, Math.PI * 2);
          ctx.fill();
        }
      };

      drawWireCube(
        outerScale,
        Math.sin(t * 0.7) * 0.08,
        dark ? 'rgba(244, 63, 142, 0.78)' : 'rgba(190, 18, 93, 0.74)',
        dark ? 'rgba(225, 29, 116, 0.08)' : 'rgba(190, 18, 93, 0.06)',
        1.8 * dpr,
      );

      drawWireCube(
        innerScale,
        -Math.sin(t * 0.7) * 0.1,
        dark ? 'rgba(238, 240, 246, 0.56)' : 'rgba(18, 20, 30, 0.45)',
        dark ? 'rgba(238, 240, 246, 0.05)' : 'rgba(18, 20, 30, 0.04)',
        1.25 * dpr,
      );
    };

    const tick = () => {
      if (!isVisible || document.hidden) {
        rafId = 0;
        return;
      }
      pointerX += (targetX - pointerX) * 0.06;
      pointerY += (targetY - pointerY) * 0.06;
      drawScene(time);
      if (!reducedMotion) {
        time += 0.016;
        rafId = window.requestAnimationFrame(tick);
      }
    };

    const handlePointerMove = (e: PointerEvent) => {
      if (!finePointer || reducedMotion) return;
      targetX = (e.clientX / Math.max(1, window.innerWidth) - 0.5) * 2;
      targetY = (e.clientY / Math.max(1, window.innerHeight) - 0.5) * 2;
    };

    const handleVisibility = () => {
      if (!document.hidden && !reducedMotion && isVisible && !rafId) {
        rafId = window.requestAnimationFrame(tick);
      }
    };

    // Always render one composed still frame immediately (also serves reduced-motion)
    drawScene(time);

    const ro = new ResizeObserver(() => drawScene(time));
    ro.observe(canvas);

    const io = new IntersectionObserver(
      (entries) => {
        isVisible = entries[0]?.isIntersecting ?? true;
        if (isVisible && !reducedMotion && !rafId) {
          rafId = window.requestAnimationFrame(tick);
        }
      },
      { threshold: 0.05 },
    );
    io.observe(canvas);

    if (finePointer && !reducedMotion) {
      window.addEventListener('pointermove', handlePointerMove, { passive: true });
    }
    document.addEventListener('visibilitychange', handleVisibility);

    if (!reducedMotion) {
      rafId = window.requestAnimationFrame(tick);
    }

    return () => {
      if (rafId) window.cancelAnimationFrame(rafId);
      ro.disconnect();
      io.disconnect();
      window.removeEventListener('pointermove', handlePointerMove);
      document.removeEventListener('visibilitychange', handleVisibility);
    };
  }, [isDark]);

  return <canvas ref={canvasRef} className="yn-hero-canvas" aria-hidden="true" />;
}
