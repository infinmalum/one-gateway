# Frontend TypeScript migration

All three themes use TypeScript source files, TypeScript 7, and Vite 8. Each `build` script runs `typecheck` before bundling.

The migration is complete: no JavaScript or JSX source files remain in the three themes, including their Vite configurations, and no source files use `@ts-nocheck`, `@ts-ignore`, or `@ts-expect-error`. Existing code was updated for the current React and UI library types, including MUI 7 layout props and Semi UI form controls.

Run `npm run build` in `web/default`, `web/air`, and `web/berry` to check types and generate production bundles. All three builds pass.

The `default` and `air` themes stay on React 18 because `semantic-ui-react` 2.1.5 declares support through React 18. The `berry` theme uses React 19.
