import { forwardRef, type ReactNode } from 'react';

// material-ui
import { useTheme } from '@mui/material/styles';
import { Card, CardContent, CardHeader, Divider, Typography } from '@mui/material';
import type { CardProps } from '@mui/material/Card';
import type { SxProps, Theme } from '@mui/material/styles';

// ==============================|| CUSTOM SUB CARD ||============================== //

type SubCardProps = Omit<CardProps, 'title' | 'content'> & {
  children?: ReactNode;
  content?: boolean;
  contentClass?: string;
  darkTitle?: boolean;
  secondary?: ReactNode;
  contentSX?: SxProps<Theme>;
  title?: ReactNode;
  subTitle?: ReactNode;
};

const SubCard = forwardRef<HTMLDivElement, SubCardProps>(
  ({ children, content = true, contentClass, darkTitle, secondary, sx, contentSX, title, subTitle, ...others }, ref) => {
    const theme = useTheme();

    return (
      <Card
        ref={ref}
        sx={[{
          border: theme.typography.SubCard.border,
          ':hover': {
            boxShadow: '0 2px 14px 0 rgb(32 40 45 / 8%)'
          }
        }, ...(Array.isArray(sx) ? sx : sx ? [sx] : [])]}
        {...others}
      >
        {/* card header and action */}
        {!darkTitle && title && (
          <CardHeader sx={{ p: 2.5 }} title={<Typography variant="h5">{title}</Typography>} action={secondary} subheader={subTitle} />
        )}
        {darkTitle && title && (
          <CardHeader sx={{ p: 2.5 }} title={<Typography variant="h4">{title}</Typography>} action={secondary} subheader={subTitle} />
        )}

        {/* content & header divider */}
        {title && (
          <Divider
            sx={{
              opacity: 1
              // borderColor: theme.palette.primary.light
            }}
          />
        )}

        {/* card content */}
        {content && (
          <CardContent sx={[{ p: 2.5 }, ...(Array.isArray(contentSX) ? contentSX : contentSX ? [contentSX] : [])]} className={contentClass || ''}>
            {children}
          </CardContent>
        )}
        {!content && children}
      </Card>
    );
  }
);

export default SubCard;
