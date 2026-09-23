import Grid from '@mui/material/Grid';
import type { GridProps, GridSize } from '@mui/material/Grid';
import type { CSSProperties } from 'react';

type LegacyGridProps = Omit<GridProps, 'direction' | 'size'> & {
  xs?: GridSize | true;
  sm?: GridSize | true;
  md?: GridSize | true;
  lg?: GridSize | true;
  xl?: GridSize | true;
  item?: boolean;
  zeroMinWidth?: boolean;
  direction?: CSSProperties['flexDirection'];
  justifyContent?: CSSProperties['justifyContent'];
  alignItems?: CSSProperties['alignItems'];
  paddingTop?: CSSProperties['paddingTop'];
};

export default function LegacyGrid({ xs, sm, md, lg, xl, item: _item, zeroMinWidth, direction, justifyContent, alignItems, paddingTop, sx, ...props }: LegacyGridProps) {
  const size = Object.fromEntries(
    Object.entries({ xs, sm, md, lg, xl })
      .filter(([, value]) => value !== undefined)
      .map(([key, value]) => [key, value === true ? 'grow' : value]),
  );
  return <Grid {...props} size={size} sx={[{
    ...(zeroMinWidth && { minWidth: 0 }),
    ...(direction && { flexDirection: direction }),
    ...(justifyContent && { justifyContent }),
    ...(alignItems && { alignItems }),
    ...(paddingTop !== undefined && { paddingTop })
  }, ...(Array.isArray(sx) ? sx : sx ? [sx] : [])]} />;
}
