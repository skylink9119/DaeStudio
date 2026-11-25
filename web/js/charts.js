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

  renderSVG(width = 480, height = 74) {
    const pad = 4;
    const h = height - pad * 2;
    const maxVal = Math.max(64 * 1024, ...this.downSeries, ...this.upSeries) * 1.15;

    const toPts = (arr) =>
      arr.map((v, i) => [
        ((i / (this.maxPoints - 1)) * width).toFixed(1),
        (pad + h - (v / maxVal) * h).toFixed(1),
      ]);

    const downPts = toPts(this.downSeries);
    const upPts = toPts(this.upSeries);
    const line = (pts) => pts.map((p, i) => `${i === 0 ? "M" : "L"}${p[0]},${p[1]}`).join(" ");
    const area = (pts) => `${line(pts)} L${width},${pad + h} L0,${pad + h} Z`;

    return `
      <svg viewBox="0 0 ${width} ${height}" width="100%" height="${height}" preserveAspectRatio="none">
        <defs>
          <linearGradient id="dlGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="#2f81f7" stop-opacity="0.28"/>
            <stop offset="100%" stop-color="#2f81f7" stop-opacity="0"/>
          </linearGradient>
        </defs>
        <path d="${area(downPts)}" fill="url(#dlGrad)"/>
        <path d="${line(downPts)}" fill="none" stroke="#2f81f7" stroke-width="1.8"/>
        <path d="${line(upPts)}" fill="none" stroke="#3fb950" stroke-width="1.4" stroke-dasharray="3 2"/>
      </svg>
    `;
  }
}
