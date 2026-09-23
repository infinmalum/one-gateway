import type { CSSProperties } from 'react';
import type { SxProps } from '@mui/system';
import type { Theme } from '@mui/material/styles';

declare module '@mui/material/styles' {
  interface TypographyVariants {
    customInput: SxProps<Theme>;
    otherInput: SxProps<Theme>;
    mainContent: CSSProperties;
    menuCaption: CSSProperties;
    subMenuCaption: CSSProperties;
    commonAvatar: CSSProperties;
    smallAvatar: CSSProperties;
    mediumAvatar: CSSProperties;
    largeAvatar: CSSProperties;
    menuButton: CSSProperties;
    menuChip: { background: string };
    CardWrapper: CSSProperties;
    SubCard: CSSProperties;
  }

  interface TypographyVariantsOptions {
    customInput?: SxProps<Theme>;
    otherInput?: SxProps<Theme>;
    mainContent?: CSSProperties;
    menuCaption?: CSSProperties;
    subMenuCaption?: CSSProperties;
    commonAvatar?: CSSProperties;
    smallAvatar?: CSSProperties;
    mediumAvatar?: CSSProperties;
    largeAvatar?: CSSProperties;
    menuButton?: CSSProperties;
    menuChip?: { background: string };
    CardWrapper?: CSSProperties;
    SubCard?: CSSProperties;
  }
}
