# Frontend TypeScript migration

The `default` theme (the only frontend) uses TypeScript source files, TypeScript 7, and Vite 8. Its `build` script runs `typecheck` before bundling.

The migration is complete: no JavaScript or JSX source files remain, including the Vite configuration, and no source files use `@ts-nocheck`, `@ts-ignore`, or `@ts-expect-error`. Existing code was updated for the current React and UI library types.

Run `npm run build` in `web/default` to check types and generate the production bundle.

The theme stays on React 18 because `semantic-ui-react` 2.1.5 declares support through React 18.

Historical note: this repository previously shipped three themes (`default`, `air`, `berry`); `air` and `berry` were removed when the frontend was consolidated to a single theme.
