// Glue only: prices from /api/prices, compute in cycles.wasm, charts by ECharts.
// TinyGo's loader (wasm_exec.js) is prepended to this bundle by build.sh.
import echarts from './charts.js';

const $ = id => document.getElementById(id);
const showStatus = (message, isError = false) => {
  $('status').textContent = message;
  $('status').classList.toggle('error', isError);
  $('summary').hidden = true;
};
const VIRIDIS_STOPS = [[68, 1, 84], [59, 82, 139], [33, 145, 140], [94, 201, 98], [253, 231, 37]];
const viridis = t => {
  const i = Math.min(3, Math.floor(t * 4)), fraction = t * 4 - i;
  return `rgb(${VIRIDIS_STOPS[i].map((c, k) => Math.round(c + (VIRIDIS_STOPS[i + 1][k] - c) * fraction))})`;
};
const formatThreshold = v => (Number.isInteger(v) ? v.toFixed(1) : String(v));
const bandLabel = (thresholds, band) =>
  `[${formatThreshold(thresholds[band])}, ${band + 1 < thresholds.length ? formatThreshold(thresholds[band + 1]) + ')' : '∞)'}`;
const parseThresholds = text => text.split(/[\s,]+/).filter(Boolean).map(Number).sort((a, b) => a - b);

const SANS = "'72', system-ui, sans-serif", MONO = "'72 Mono', ui-monospace, monospace";
const MUTED = '#5d6575', FAINT = '#8b92a1', CLOSE_COLOR = '#2a7fc4';
const axisStyle = { axisLabel: { fontFamily: MONO, fontSize: 11, color: FAINT }, nameTextStyle: { fontFamily: SANS, fontSize: 11, color: MUTED },
  axisLine: { lineStyle: { color: '#e4e6ec' } }, axisTick: { show: false }, splitLine: { lineStyle: { color: '#eceef2' } } };
const baseOption = { textStyle: { fontFamily: SANS }, tooltip: { renderMode: 'richText' } }; // canvas tooltip: no HTML at all
const phone = matchMedia('(max-width: 640px)'); // matches the phone layout in style.css
let priceChart, boxChart, renderBox; // charts are created once #results is visible, so ECharts can measure it
const resizeCharts = new ResizeObserver(() => { priceChart?.resize(); boxChart?.resize(); }); // flex/grid can resize charts without a window resize
['price-chart', 'box-chart'].forEach(id => resizeCharts.observe($(id)));
phone.addEventListener('change', () => renderBox?.());

const go = new Go();
const wasmReady = WebAssembly.instantiateStreaming(fetch('cycles.wasm'), go.importObject).then(result => { go.run(result.instance); });

const showRanges = () => {
  const thresholds = parseThresholds($('thresholds').value);
  $('ranges').textContent = thresholds.length && thresholds.every(Number.isFinite)
    ? 'Ranges: ' + thresholds.map((_, band) => bandLabel(thresholds, band)).join('  ') : 'Space-separated, any count';
};
$('thresholds').addEventListener('input', showRanges);
showRanges();

// HTML legend; a click hides/shows that series like the built-in ECharts legend does
const legendItem = (name, swatch, label, count) => {
  const item = document.createElement('button');
  item.type = 'button';
  item.setAttribute('aria-pressed', 'true');
  const mark = document.createElement('span');
  mark.className = swatch.className;
  mark.style.background = swatch.color;
  item.append(mark, label);
  if (count !== undefined) {
    const n = document.createElement('span');
    n.className = 'count';
    n.textContent = count;
    item.append(' ', n);
  }
  item.addEventListener('click', () => {
    priceChart.dispatchAction({ type: 'legendToggleSelect', name });
    item.setAttribute('aria-pressed', String(item.getAttribute('aria-pressed') !== 'true'));
  });
  return item;
};

$('form').addEventListener('submit', async event => {
  event.preventDefault();
  const ticker = $('ticker').value.trim().toUpperCase(), years = Number($('years').value);
  const thresholds = parseThresholds($('thresholds').value);
  if (!thresholds.length || thresholds.some(v => !Number.isFinite(v))) return showStatus('Thresholds: one or more numbers, e.g. 5 10 20', true);
  showStatus('Loading…');
  try {
    const response = await fetch('/api/prices?' + new URLSearchParams({ ticker, years }));
    if (!response.ok) return showStatus(await response.text(), true);
    const { company, dates, closes } = await response.json();
    if (!closes.length) return showStatus(`No data found for ticker ${ticker}.`, true);
    await wasmReady;
    const { average, cycles, boxes } = findCycles(dates.map(Date.parse), closes, thresholds), displayName = `${company} (${ticker})`;
    const labels = thresholds.map((_, band) => bandLabel(thresholds, band)), colors = thresholds.map((_, band) => viridis(band / thresholds.length));
    const counts = thresholds.map((_, band) => cycles.filter(c => c.band === band).length);

    showStatus('');
    $('company').textContent = company;
    $('symbol').textContent = ticker;
    document.querySelectorAll('.avg-years').forEach(el => { el.textContent = years; });
    $('average').textContent = average.toFixed(2);
    $('cycle-count').textContent = cycles.length;
    $('summary').hidden = false;
    $('price-title').textContent = `Stock Prices for ${displayName}`;
    $('box-title').textContent = `Stock cycle period over the last ${years} year(s)`;
    $('box-subtitle').textContent = displayName;
    const caption = document.createElement('span');
    caption.className = 'legend-caption';
    caption.textContent = 'Minima between (%)';
    $('legend').replaceChildren(caption, legendItem('Close', { className: 'swatch swatch-line', color: CLOSE_COLOR }, 'Close'),
      ...labels.map((label, band) => {
        const text = document.createElement('span');
        text.className = 'band';
        text.textContent = label;
        return legendItem(label, { className: 'swatch', color: colors[band] }, text, counts[band]);
      }));
    $('results').hidden = false;
    await document.fonts.ready; // the canvas only draws with fonts that have finished loading
    [priceChart, boxChart] = [priceChart, boxChart].map((chart, i) => chart ?? echarts.init($(['price-chart', 'box-chart'][i])));

    const series = [{ name: 'Close', type: 'line', data: dates.map((d, i) => [d, closes[i]]), showSymbol: false, color: CLOSE_COLOR, lineStyle: { width: 1.3 } }];
    thresholds.forEach((_, band) => {
      const segments = [], markers = [];
      for (const { peak, low, recovery, drop, days } of cycles.filter(c => c.band === band)) {
        segments.push([dates[peak], closes[peak]], [dates[low], closes[peak]], [dates[low], '-'],
          [dates[low], closes[low]], [dates[low], closes[recovery]], [dates[recovery], closes[recovery]], [dates[recovery], '-']);
        const tooltip = `${dates[peak]} → ${dates[recovery]}\ndrop ${drop.toFixed(2)}% · ${(days / 7).toFixed(1)} weeks`;
        markers.push(...[peak, low, recovery].map(i => ({ value: [dates[i], closes[i]], tooltip })));
      }
      series.push({ name: labels[band], type: 'line', data: segments, showSymbol: false, color: colors[band], silent: true, lineStyle: { width: 1.3, opacity: 0.9 } },
        { name: labels[band], type: 'scatter', data: markers, color: colors[band], symbolSize: 5 });
    });
    // axis trigger: tapping anywhere shows that day's close plus any cycle marked on it (5px markers are too small for fingers)
    const priceTooltip = params => {
      const close = params.find(p => p.seriesName === 'Close');
      if (!close) return '';
      const [date, price] = close.data, cycleLines = params.filter(p => p.seriesType === 'scatter' && p.data.value[0] === date).map(p => p.data.tooltip);
      return [`${date} · close ${price.toFixed(2)}`, ...new Set(cycleLines)].join('\n');
    };
    priceChart.setOption({ ...baseOption, tooltip: { ...baseOption.tooltip, trigger: 'axis', formatter: priceTooltip },
      legend: { show: false }, grid: { left: 48, right: 12, top: 28, bottom: 44 }, // the HTML legend drives this one
      xAxis: { type: 'time', name: 'Date', nameLocation: 'middle', nameGap: 28, ...axisStyle, splitLine: { show: false } },
      yAxis: { type: 'value', scale: true, name: 'Price', ...axisStyle, axisLine: { show: false } },
      dataZoom: [{ type: 'inside', preventDefaultMouseMove: false }], series }, true); // else touch drags never scroll the page

    // phones get horizontal boxes so the range labels have room; re-rendered when the layout switches
    renderBox = () => {
      const horizontal = phone.matches;
      $('box-chart').style.height = horizontal ? `${Math.max(160, 48 * thresholds.length + 70)}px` : '';
      boxChart.resize();
      const categoryAxis = { type: 'category', data: labels, name: 'Minima between (%)', ...axisStyle, splitLine: { show: false },
        axisLabel: { ...axisStyle.axisLabel, color: MUTED, interval: 0, hideOverlap: false, lineHeight: 15,
          formatter: (label, band) => `${label}\n{n|n = ${counts[band]}}`, rich: { n: { fontFamily: MONO, fontSize: 10, color: FAINT } } } };
      const valueAxis = { type: 'value', name: 'Stock cycle period (weeks)', ...axisStyle, axisLine: { show: false } };
      boxChart.setOption({ ...baseOption,
        grid: horizontal ? { left: 92, right: 14, top: 28, bottom: 40 } : { left: 44, right: 8, top: 28, bottom: 64 },
        xAxis: horizontal ? { ...valueAxis, nameLocation: 'middle', nameGap: 26 } : { ...categoryAxis, nameLocation: 'middle', nameGap: 46 },
        yAxis: horizontal ? { ...categoryAxis, inverse: true, nameLocation: 'start', nameTextStyle: { ...axisStyle.nameTextStyle, align: 'right' } } : valueAxis, // inverse: 'start' is the top
        series: [{ type: 'boxplot', data: boxes.map((box, band) => ({ value: box.stats,
          itemStyle: { color: colors[band].replace('rgb', 'rgba').replace(')', ',.22)'), borderColor: colors[band], borderWidth: 1.3 } })) },
        { type: 'scatter', symbol: 'emptyCircle', symbolSize: 6, data: boxes.flatMap((box, band) =>
          box.outliers.map(v => ({ value: horizontal ? [v, band] : [band, v], itemStyle: { color: colors[band] } }))) }] }, true);
    };
    renderBox();
  } catch (error) {
    showStatus('Request failed: ' + error.message, true);
  }
});
