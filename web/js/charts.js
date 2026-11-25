export class TrafficChart {
  constructor(maxPoints = 36) {
    this.maxPoints = maxPoints;
    this.downSeries = Array(maxPoints).fill(0);
    this.upSeries = Array(maxPoints).fill(0);
  }

  push(upBps, downBps) {
    this.upSeries.push(Math.max(0, upBps || 0));
    this.downSeries.push(Math.max(0, downBps || 0));
    if (this.upSeries.length > this.maxPoints) this.upSeries.shift();
    if (this.downSeries.length > this.maxPoints) this.downSeries.shift();
  }

  renderSVG(width = 480, height = 82) {
    const padTop = 6;
    const padBottom = 5;
    const h = height - padTop - padBottom;
    const baseY = padTop + h;
    const maxVal = Math.max(64 * 1024, ...this.downSeries, ...this.upSeries) * 1.16;

    const toPts = (arr) =>
      arr.map((v, i) => [
        (i / (this.maxPoints - 1)) * width,
        Math.min(baseY, Math.max(padTop, baseY - (v / maxVal) * h)),
      ]);

    const smoothPath = (pts) => {
      if (!pts.length) return "";
      let d = `M${pts[0][0].toFixed(1)},${pts[0][1].toFixed(1)}`;
      for (let i = 0; i < pts.length - 1; i++) {
        const p0 = pts[i];
        const p1 = pts[i + 1];
        const cx = ((p0[0] + p1[0]) / 2).toFixed(1);
        d += ` C${cx},${p0[1].toFixed(1)} ${cx},${p1[1].toFixed(1)} ${p1[0].toFixed(1)},${p1[1].toFixed(1)}`;
      }
      return d;
    };

    const downPts = toPts(this.downSeries);
    const upPts = toPts(this.upSeries);
    const downLine = smoothPath(downPts);
    const upLine = smoothPath(upPts);
    const downArea = `${downLine} L${width},${baseY} L0,${baseY} Z`;
    const upArea = `${upLine} L${width},${baseY} L0,${baseY} Z`;
    const lastDown = downPts[downPts.length - 1];
    const grid1 = (padTop + h * 0.33).toFixed(1);
    const grid2 = (padTop + h * 0.66).toFixed(1);

    return `
      <svg class="traffic-svg" viewBox="0 0 ${width} ${height}" width="100%" height="${height}" preserveAspectRatio="none">
        <defs>
          <linearGradient id="dlGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="var(--primary)" stop-opacity="0.34"/>
            <stop offset="85%" stop-color="var(--primary)" stop-opacity="0.03"/>
            <stop offset="100%" stop-color="var(--primary)" stop-opacity="0"/>
          </linearGradient>
          <linearGradient id="ulGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="var(--ok-text)" stop-opacity="0.16"/>
            <stop offset="100%" stop-color="var(--ok-text)" stop-opacity="0"/>
          </linearGradient>
        </defs>
        <line x1="0" y1="${grid1}" x2="${width}" y2="${grid1}" stroke="currentColor" stroke-opacity="0.06" stroke-dasharray="3 3"/>
        <line x1="0" y1="${grid2}" x2="${width}" y2="${grid2}" stroke="currentColor" stroke-opacity="0.06" stroke-dasharray="3 3"/>
        <line x1="0" y1="${baseY}" x2="${width}" y2="${baseY}" stroke="currentColor" stroke-opacity="0.09"/>
        <path d="${upArea}" fill="url(#ulGrad)"/>
        <path d="${downArea}" fill="url(#dlGrad)"/>
        <path d="${upLine}" fill="none" stroke="var(--ok-text)" stroke-width="1.5" stroke-dasharray="3.5 2.5" stroke-linecap="round"/>
        <path d="${downLine}" fill="none" stroke="var(--primary)" stroke-width="2.1" stroke-linecap="round"/>
        <circle cx="${(width - 2).toFixed(1)}" cy="${lastDown[1].toFixed(1)}" r="2.8" fill="var(--primary)"/>
      </svg>
    `;
  }
}
