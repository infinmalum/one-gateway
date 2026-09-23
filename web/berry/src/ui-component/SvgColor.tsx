import { forwardRef } from 'react';

import Box from '@mui/material/Box';
import type { BoxProps } from '@mui/material/Box';

// ----------------------------------------------------------------------

type SvgColorProps = Omit<BoxProps, 'component'> & { src?: string };

const SvgColor = forwardRef<HTMLSpanElement, SvgColorProps>(({ src, sx, ...other }, ref) => (
  <Box
    component="span"
    className="svg-color"
    ref={ref}
    sx={[{
      width: 24,
      height: 24,
      display: 'inline-block',
      bgcolor: 'currentColor',
      mask: `url(${src}) no-repeat center / contain`,
      WebkitMask: `url(${src}) no-repeat center / contain`
    }, ...(Array.isArray(sx) ? sx : sx ? [sx] : [])]}
    {...other}
  />
));

export default SvgColor;
