// contexts/User/index.jsx

import React from 'react';
import { initialState, reducer } from './reducer';

export const StatusContext = React.createContext<[Record<string, any>, React.Dispatch<any>]>([
  initialState,
  () => {},
]);

export const StatusProvider = ({ children }) => {
  const [state, dispatch] = React.useReducer(reducer, initialState);

  return (
    <StatusContext.Provider value={[state, dispatch]}>
      {children}
    </StatusContext.Provider>
  );
};
