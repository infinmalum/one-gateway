// material-ui
import { Link, Container, Box } from '@mui/material';
import React from 'react';
import { useAppSelector as useSelector } from 'store/hooks';

// ==============================|| FOOTER - AUTHENTICATION 2 & 3 ||============================== //

const Footer = () => {
  const siteInfo = useSelector((state: any) => state.siteInfo);

  return (
    <Container sx={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '64px' }}>
      <Box sx={{ textAlign: 'center' }}>
        {siteInfo.footer_html ? (
          <div className="custom-footer" dangerouslySetInnerHTML={{ __html: siteInfo.footer_html }}></div>
        ) : (
          <>
            <Link href="https://github.com/infinmalum/one-gateway" target="_blank">
              {siteInfo.system_name} {import.meta.env.REACT_APP_VERSION}{' '}
            </Link>
            基于{' '}
            <Link href="https://github.com/songquanpeng/one-api" target="_blank">
              One API（JustSong）
            </Link>{' '}
            ，主题 berry 来自{' '}
            <Link href="https://github.com/MartialBE" target="_blank">
              MartialBE
            </Link>{' '}；原项目采用 MIT，派生修改采用
            <Link href="https://github.com/infinmalum/one-gateway/blob/main/LICENSE"> Apache 2.0</Link>
          </>
        )}
      </Box>
    </Container>
  );
};

export default Footer;
