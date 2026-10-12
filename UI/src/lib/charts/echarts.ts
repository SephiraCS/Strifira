import { use, type ComposeOption } from 'echarts/core';
import {
	BarChart,
	CustomChart,
	GraphChart,
	LineChart,
	ScatterChart,
	type BarSeriesOption,
	type CustomSeriesOption,
	type GraphSeriesOption,
	type LineSeriesOption,
	type ScatterSeriesOption
} from 'echarts/charts';
import {
	DataZoomInsideComponent,
	DataZoomSliderComponent,
	GridComponent,
	LegendPlainComponent,
	MarkAreaComponent,
	MarkLineComponent,
	TooltipComponent,
	type DataZoomComponentOption,
	type GridComponentOption,
	type LegendComponentOption,
	type MarkAreaComponentOption,
	type MarkLineComponentOption,
	type TooltipComponentOption
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';

// Register only what Ku renders. Importing the full bundle also ships unused
// chart types, coordinate systems, components, and the SVG renderer.
use([
	BarChart,
	CustomChart,
	GraphChart,
	LineChart,
	ScatterChart,
	DataZoomInsideComponent,
	DataZoomSliderComponent,
	GridComponent,
	LegendPlainComponent,
	MarkAreaComponent,
	MarkLineComponent,
	TooltipComponent,
	CanvasRenderer
]);

export { color, graphic, init } from 'echarts/core';
export type { ECharts } from 'echarts/core';

export type ChartOption = ComposeOption<
	| BarSeriesOption
	| CustomSeriesOption
	| GraphSeriesOption
	| LineSeriesOption
	| ScatterSeriesOption
	| DataZoomComponentOption
	| GridComponentOption
	| LegendComponentOption
	| MarkAreaComponentOption
	| MarkLineComponentOption
	| TooltipComponentOption
>;
