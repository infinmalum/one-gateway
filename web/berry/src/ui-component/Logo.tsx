// material-ui
import logoLight from 'assets/images/logo.svg';
import logoDark from 'assets/images/logo-white.svg';
import { useAppSelector as useSelector } from 'store/hooks';
import { useTheme } from '@mui/material/styles';

/**
 * if you want to use image instead of <svg> uncomment following.
 *
 * import logoDark from 'assets/images/logo-dark.svg';
 * import logo from 'assets/images/logo.svg';
 *
 */

// ==============================|| LOGO SVG ||============================== //

const Logo = () => {
  const siteInfo = useSelector((state: { siteInfo: { logo?: string; system_name?: string } }) => state.siteInfo);
  const theme = useTheme();
  const logo = theme.palette.mode === 'light' ? logoLight : logoDark;

  return <img src={siteInfo.logo || logo} alt={siteInfo.system_name} height="50" />;
};

export default Logo;
