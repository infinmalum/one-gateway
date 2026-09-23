import { Link } from 'react-router-dom';
import { useDispatch } from 'react-redux';
import { useAppSelector as useSelector } from 'store/hooks';

// material-ui
import { ButtonBase } from '@mui/material';

// project imports
import Logo from 'ui-component/Logo';
import { MENU_OPEN } from 'store/actions';

// ==============================|| MAIN LOGO ||============================== //

const LogoSection = () => {
  const defaultId = useSelector((state) => state.customization.defaultId);
  const dispatch = useDispatch();
  return (
    <ButtonBase disableRipple onClick={() => dispatch({ type: MENU_OPEN, id: defaultId })} component={Link} to="/">
      <Logo />
    </ButtonBase>
  );
};

export default LogoSection;
