# ENV example

Shows the `ENV` directive: variables set in the Nimbusfile are exported
into ENTRYPOINT's (and CMD's, and every SERVICE's) environment at boot,
the same way Docker's `ENV` works for a container.

## Build

```bash
cnimbus build-disk -f Nimbusfile
```

## Boot and check

Boot `cnimbus-env-demo.iso` in any hypervisor with a serial or VGA
console attached (add `VGA true` to the Nimbusfile, or `--vga` to
`cnimbus prepare`, if you want to see it in a GUI window instead of a
serial log). You should see this line once, near the end of boot:

```
cnimbus-demo (production): hello-from-the-nimbusfile
```

## Notes

- `ENV` is config baked into the image at build time -- to change a
  value you rebuild. For config you want to change on a *running* VM
  without rebuilding, see [agent-http-kv](../agent-http-kv/) or
  [agent-vboxguest-kv](../agent-vboxguest-kv/) instead.
- Order matters for overrides: a later `ENV KEY=...` line replaces an
  earlier one with the same key, same as re-declaring a shell variable.
