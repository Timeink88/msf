"use client";

import { useEffect, useRef } from "react";
// On-demand echarts build: the full package (`import * as echarts from
// "echarts"`) shipped every chart type (~1.1MB chunk).  The app only uses
// line + sankey series with grid/tooltip/title — register exactly those.
// `EChartsOption` stays a type-only import from the full package (erased at
// build time, zero runtime cost) so existing option objects keep their
// permissive types.
import * as echarts from "echarts/core";
import { LineChart, SankeyChart } from "echarts/charts";
import { GridComponent, LegendComponent, TitleComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import type { EChartsOption } from "echarts";
import { cn } from "@/lib/utils";

echarts.use([LineChart, SankeyChart, GridComponent, LegendComponent, TitleComponent, TooltipComponent, CanvasRenderer]);

export function EChartCanvas({
  option,
  className,
  onTooltipVisibilityChange,
  lazyUpdate = false,
}: {
  option: EChartsOption;
  className?: string;
  onTooltipVisibilityChange?: (visible: boolean) => void;
  lazyUpdate?: boolean;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    const element = containerRef.current;
    if (!element) return;

    const chart = echarts.init(element, undefined, { renderer: "canvas" });
    chartRef.current = chart;
    const resizeObserver = new ResizeObserver(() => chart.resize());
    resizeObserver.observe(element);

    const showTooltip = () => onTooltipVisibilityChange?.(true);
    const hideTooltip = () => onTooltipVisibilityChange?.(false);
    chart.on("showTip", showTooltip);
    chart.on("hideTip", hideTooltip);

    return () => {
      resizeObserver.disconnect();
      chart.off("showTip", showTooltip);
      chart.off("hideTip", hideTooltip);
      chart.dispose();
      chartRef.current = null;
    };
  }, [onTooltipVisibilityChange]);

  useEffect(() => {
    chartRef.current?.setOption(option, { notMerge: false, lazyUpdate });
  }, [lazyUpdate, option]);

  return <div ref={containerRef} className={cn("h-full w-full", className)} />;
}

export { echarts };
export type { EChartsOption };
