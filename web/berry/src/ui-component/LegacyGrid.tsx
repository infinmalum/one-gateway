import React from 'react';
import Grid from '@mui/material/Grid';

export default function LegacyGrid({ xs, sm, md, lg, xl, ...props }) {
  const size = Object.fromEntries(
    Object.entries({ xs, sm, md, lg, xl }).filter(([, value]) => value !== undefined),
  );
  return <Grid {...props} size={size} />;
}
