import Stack from '@mui/material/Stack';
import type { StackProps } from '@mui/material/Stack';
import type { CSSProperties } from 'react';

type LegacyStackProps = StackProps & {
  alignItems?: CSSProperties['alignItems'];
  justifyContent?: CSSProperties['justifyContent'];
  padding?: CSSProperties['padding'];
  paddingTop?: CSSProperties['paddingTop'];
  paddingBottom?: CSSProperties['paddingBottom'];
  marginTop?: CSSProperties['marginTop'];
  mb?: number | string;
};

export default function LegacyStack({ alignItems, justifyContent, padding, paddingTop, paddingBottom, marginTop, mb, sx, ...props }: LegacyStackProps) {
  return <Stack {...props} sx={[{
    ...(alignItems && { alignItems }),
    ...(justifyContent && { justifyContent }),
    ...(padding !== undefined && { padding }),
    ...(paddingTop !== undefined && { paddingTop }),
    ...(paddingBottom !== undefined && { paddingBottom }),
    ...(marginTop !== undefined && { marginTop }),
    ...(mb !== undefined && { mb })
  }, ...(Array.isArray(sx) ? sx : sx ? [sx] : [])]} />;
}
