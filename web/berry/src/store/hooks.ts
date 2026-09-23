import { useSelector } from 'react-redux';
import type reducer from './reducer';

export type RootState = ReturnType<typeof reducer>;
export const useAppSelector = useSelector.withTypes<RootState>();
