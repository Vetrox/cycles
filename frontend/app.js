// Glue only: prices from /api/prices, compute in cycles.wasm, charts by ECharts.
// TinyGo's loader (wasm_exec.js) is prepended to this bundle by build.sh.
import echarts from './charts.js';

const $ = id => document.getElementById(id);
const showStatus = message => { $('status').textContent = message; };
const VIRIDIS_STOPS = [[68, 1, 84], [59, 82, 139], [33, 145, 140], [94, 201, 98], [253, 231, 37]];
const viridis = t => {
  const i = Math.min(3, Math.floor(t * 4)), fraction = t * 4 - i;
  return `rgb(${VIRIDIS_STOPS[i].map((c, k) => Math.round(c + (VIRIDIS_STOPS[i + 1][k] - c) * fraction))})`;
};
const formatThreshold = v => (Number.isInteger(v) ? v.toFixed(1) : String(v));
const bandLabel = (thresholds, band) =>
  `[${formatThreshold(thresholds[band])}, ${band + 1 < thresholds.length ? formatThreshold(thresholds[band + 1]) + ')' : '∞)'}`;
const baseOption = { textStyle: { fontFamily: 'system-ui, sans-serif' }, tooltip: { renderMode: 'richText' }, // canvas tooltip: no HTML at all
  title: { left: 'center', textStyle: { fontWeight: 500 } }, grid: { left: 64, right: 24, top: 70, bottom: 40 } };
const [priceChart, boxChart] = ['price-chart', 'box-chart'].map(id => echarts.init($(id)));
addEventListener('resize', () => { priceChart.resize(); boxChart.resize(); });

const go = new Go();
const wasmReady = WebAssembly.instantiateStreaming(fetch('cycles.wasm'), go.importObject).then(result => { go.run(result.instance); });

$('form').addEventListener('submit', async event => {
  event.preventDefault();
  const ticker = $('ticker').value.trim().toUpperCase(), years = Number($('years').value);
  const thresholds = $('thresholds').value.split(/[\s,]+/).filter(Boolean).map(Number).sort((a, b) => a - b);
  if (!thresholds.length || thresholds.some(v => !Number.isFinite(v))) return showStatus('Thresholds: one or more numbers, e.g. 5 10 20');
  showStatus('Loading…');
  try {
    const response = await fetch('/api/prices?' + new URLSearchParams({ ticker, years }));
    if (!response.ok) return showStatus(await response.text());
    const { company, dates, closes } = await response.json();
    if (!closes.length) return showStatus(`No data found for ticker ${ticker}.`);
    await wasmReady;
    const { average, cycles, boxes } = findCycles(dates.map(Date.parse), closes, thresholds), displayName = `${company} (${ticker})`;
    showStatus(`${displayName} · average close over ${years} years: ${average.toFixed(2)} · ${cycles.length} cycles`);
    const series = [{ name: 'Close', type: 'line', data: dates.map((d, i) => [d, closes[i]]), showSymbol: false, color: '#1f77b4', lineStyle: { width: 1.5 } }];
    thresholds.forEach((_, band) => {
      const segments = [], markers = [], color = viridis(band / thresholds.length);
      for (const { peak, low, recovery, drop, days } of cycles.filter(c => c.band === band)) {
        segments.push([dates[peak], closes[peak]], [dates[low], closes[peak]], [dates[low], '-'],
          [dates[low], closes[low]], [dates[low], closes[recovery]], [dates[recovery], closes[recovery]], [dates[recovery], '-']);
        const tooltip = `${dates[peak]} → ${dates[recovery]}\ndrop ${drop.toFixed(2)}% · ${(days / 7).toFixed(1)} weeks`;
        markers.push(...[peak, low, recovery].map(i => ({ value: [dates[i], closes[i]], tooltip })));
      }
      series.push({ name: bandLabel(thresholds, band), type: 'line', data: segments, showSymbol: false, color, silent: true, lineStyle: { width: 2 } },
        { name: bandLabel(thresholds, band), type: 'scatter', data: markers, color, symbolSize: 5, tooltip: { formatter: params => params.data.tooltip } });
    });
    priceChart.setOption({ ...baseOption, title: { ...baseOption.title, text: `Stock Prices for ${displayName}` }, grid: { ...baseOption.grid, top: 84 },
      legend: { top: 40, left: 'center', icon: 'roundRect', formatter: name => (name === 'Close' ? name : `Minima ${name} %`) },
      xAxis: { type: 'time' }, yAxis: { type: 'value', scale: true, name: 'Price' },
      dataZoom: [{ type: 'inside' }], series }, true);
    boxChart.setOption({ ...baseOption, title: { ...baseOption.title, text: `Stock cycle period over the last ${years} year(s)`, subtext: displayName },
      xAxis: { type: 'category', data: thresholds.map((_, band) => bandLabel(thresholds, band)) }, yAxis: { type: 'value', name: 'Weeks' },
      series: [{ type: 'boxplot', data: boxes.map((box, band) => {
        const color = viridis(band / thresholds.length);
        return { value: box.stats, itemStyle: { color: color.replace('rgb', 'rgba').replace(')', ',.35)'), borderColor: color } };
      }) },
      { type: 'scatter', data: boxes.flatMap((box, band) => box.outliers.map(v => [band, v])), color: '#555', symbolSize: 6 }] }, true);
  } catch (error) {
    showStatus('Request failed: ' + error.message);
  }
});
