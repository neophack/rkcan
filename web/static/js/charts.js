/**
 * charts.js - Lightweight Canvas chart library for system monitoring.
 */

var ChartColors = Object.freeze({
    CPU:         '#3794ff',
    Memory:      '#4ec9b0',
    NetworkRX:   '#eab308',
    NetworkTX:   '#d18616',
    Temperature: '#ef4444',
    Background:  '#1e1e1e',
    GridLine:    '#2b2b2b',
    Text:        '#e2e8f0',
    TextDim:     '#8b8b8b',
});

function niceMax(max) {
    if (max <= 0) return 1;
    if (max < 1) return 1;
    var d = Math.pow(10, Math.floor(Math.log10(max)));
    return Math.ceil(max / d) * d;
}

function LineChart(canvas, opts) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');

    var defaults = {
        maxPoints:  300,
        yMin:       0,
        yMax:       100,
        yLabel:     '%',
        gridColor:  ChartColors.GridLine,
        lineColor:  ChartColors.CPU,
        fillColor:  'rgba(59,130,246,0.1)',
        lineWidth:  1.5,
        showGrid:   true,
        showValue:  true,
        animate:    true,
        autoScale:  false,
        gridLines:  4,
        font:       '11px Consolas, monospace',
        background: ChartColors.Background,
        padding:    { top: 8, right: 8, bottom: 20, left: 40 },
    };
    this.opts = {};
    for (var k in defaults) this.opts[k] = defaults[k];
    for (var k2 in opts) this.opts[k2] = opts[k2];

    this.series = [];
    this.series.push({
        data: [],
        color: this.opts.lineColor,
        fill: this.opts.fillColor,
        label: '',
    });

    this._raf = null;
    this._destroyed = false;

    var self = this;
    this._resizeObserver = new ResizeObserver(function () { self.resize(); });
    this._resizeObserver.observe(this.canvas.parentElement || this.canvas);

    this.resize();
    if (this.opts.animate) this._startLoop();
}

LineChart.prototype.addSeries = function (cfg) {
    cfg = cfg || {};
    var idx = this.series.length;
    this.series.push({
        data: [],
        color: cfg.color || ChartColors.CPU,
        fill: cfg.fillColor || 'transparent',
        label: cfg.label || '',
    });
    return idx;
};

LineChart.prototype.addPoint = function (value, seriesIndex) {
    seriesIndex = seriesIndex || 0;
    var s = this.series[seriesIndex];
    if (!s) return;
    s.data.push(value);
    if (s.data.length > this.opts.maxPoints) s.data.shift();
    if (!this.opts.animate) this.render();
};

LineChart.prototype.setData = function (arr, seriesIndex) {
    seriesIndex = seriesIndex || 0;
    var s = this.series[seriesIndex];
    if (!s) return;
    s.data = arr.slice(-this.opts.maxPoints);
    if (!this.opts.animate) this.render();
};

LineChart.prototype.resize = function () {
    var rect = this.canvas.getBoundingClientRect();
    var dpr = window.devicePixelRatio || 1;
    this.canvas.width = rect.width * dpr;
    this.canvas.height = rect.height * dpr;
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.width = rect.width;
    this.height = rect.height;
    if (!this.opts.animate) this.render();
};

LineChart.prototype.render = function () {
    if (this._destroyed) return;
    var ctx = this.ctx;
    var o = this.opts;
    var pad = o.padding;

    var plotW = this.width - pad.left - pad.right;
    var plotH = this.height - pad.top - pad.bottom;

    var yMin = o.yMin;
    var yMax = o.yMax;
    if (o.autoScale) {
        var allMin = Infinity, allMax = -Infinity;
        for (var si = 0; si < this.series.length; si++) {
            var sd = this.series[si].data;
            for (var di = 0; di < sd.length; di++) {
                if (sd[di] < allMin) allMin = sd[di];
                if (sd[di] > allMax) allMax = sd[di];
            }
        }
        if (allMin !== Infinity) {
            yMin = o.yMin;
            if (allMin < yMin) yMin = Math.floor(allMin);
            yMax = niceMax(allMax);
        }
    }
    var yRange = yMax - yMin || 1;

    ctx.fillStyle = o.background;
    ctx.fillRect(0, 0, this.width, this.height);

    if (o.showGrid) {
        ctx.strokeStyle = o.gridColor;
        ctx.lineWidth = 0.5;
        ctx.font = o.font;
        ctx.fillStyle = ChartColors.TextDim;
        ctx.textAlign = 'right';
        ctx.textBaseline = 'middle';

        for (var i = 0; i <= o.gridLines; i++) {
            var frac = i / o.gridLines;
            var y = pad.top + frac * plotH;
            var val = yMax - frac * yRange;

            ctx.beginPath();
            ctx.moveTo(pad.left, y);
            ctx.lineTo(pad.left + plotW, y);
            ctx.stroke();

            var decimals = (val < 1 && val > 0) ? 1 : 0;
            ctx.fillText(
                val.toFixed(decimals) + o.yLabel,
                pad.left - 4, y
            );
        }
    }

    for (var j = 0; j < this.series.length; j++) {
        if (this.series[j].data.length < 2) continue;
        this._drawSeries(ctx, this.series[j], pad, plotW, plotH, yMin, yRange);
    }

    if (o.showValue && this.series[0].data.length > 0) {
        var last = this.series[0].data[this.series[0].data.length - 1];
        ctx.font = 'bold 13px Consolas, monospace';
        ctx.fillStyle = ChartColors.Text;
        ctx.textAlign = 'right';
        ctx.textBaseline = 'top';
        ctx.fillText(
            last.toFixed(1) + o.yLabel,
            this.width - pad.right - 2, pad.top + 2
        );
    }
};

LineChart.prototype.destroy = function () {
    this._destroyed = true;
    if (this._raf) cancelAnimationFrame(this._raf);
    if (this._resizeObserver) this._resizeObserver.disconnect();
};

LineChart.prototype._drawSeries = function (ctx, s, pad, plotW, plotH, yMin, yRange) {
    var pts = s.data;
    var len = pts.length;
    var step = plotW / (this.opts.maxPoints - 1);
    var xStart = pad.left + plotW - (len - 1) * step;

    var toY = function (v) {
        return pad.top + plotH - ((v - yMin) / yRange) * plotH;
    };

    ctx.beginPath();
    ctx.moveTo(xStart, toY(pts[0]));
    for (var i = 1; i < len; i++) {
        ctx.lineTo(xStart + i * step, toY(pts[i]));
    }

    ctx.strokeStyle = s.color;
    ctx.lineWidth = this.opts.lineWidth;
    ctx.lineJoin = 'round';
    ctx.stroke();

    if (s.fill && s.fill !== 'transparent') {
        ctx.lineTo(xStart + (len - 1) * step, pad.top + plotH);
        ctx.lineTo(xStart, pad.top + plotH);
        ctx.closePath();
        ctx.fillStyle = s.fill;
        ctx.fill();
    }
};

LineChart.prototype._startLoop = function () {
    var self = this;
    var tick = function () {
        if (self._destroyed) return;
        self.render();
        self._raf = requestAnimationFrame(tick);
    };
    self._raf = requestAnimationFrame(tick);
};
