declare module '*.svg?react' {
  import type { ComponentType, SVGProps } from 'react';
  const Component: ComponentType<SVGProps<SVGSVGElement>>;
  export default Component;
}
