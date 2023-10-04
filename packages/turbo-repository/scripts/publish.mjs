#!/usr/bin/env node

import path from "path";
import { execa } from "execa";
import { copy } from "fs-extra";
import { Sema } from "async-sema";
import { readFile, readdir, writeFile } from "fs/promises";

const cwd = process.cwd();

(async function () {
  try {
    const publishSema = new Sema(2);

    // TODO: version
    let version = JSON.parse(
      await readFile(path.join(cwd, "package.json"))
    ).version;

    // Copy binaries to package folders, update version, and publish
    let nativePackagesDir = path.join(cwd, "npm");
    let platforms = (await readdir(nativePackagesDir)).filter(
      (name) => !name.startsWith(".")
    );

    await Promise.all(
      platforms.map(async (platform) => {
        await publishSema.acquire();

        try {
          let binaryName = `repository.${platform}.node`;
          await copy(
            path.join(cwd, "native/@turbo", binaryName),
            path.join(nativePackagesDir, platform, binaryName)
          );
          let pkg = JSON.parse(
            await readFile(
              path.join(nativePackagesDir, platform, "package.json")
            )
          );
          pkg.version = version;
          await writeFile(
            path.join(nativePackagesDir, platform, "package.json"),
            JSON.stringify(pkg, null, 2)
          );
          // await execa(
          //   `npm`,
          //   [
          //     `publish`,
          //     `${path.join(nativePackagesDir, platform)}`,
          //     `--access`,
          //     `public`,
          //     ...(version.includes('canary') ? ['--tag', 'canary'] : []),
          //   ],
          //   { stdio: 'inherit' }
          // )
          await execa(
            `npm`,
            [
              `pack`,
              "--pack-destination=./tars",
              `${path.join(nativePackagesDir, platform)}`,
            ],
            {
              stdio: "inherit",
            }
          );
        } catch (err) {
          // don't block publishing other versions on single platform error
          console.error(`Failed to publish`, platform, err);

          if (
            err.message &&
            err.message.includes(
              "You cannot publish over the previously published versions"
            )
          ) {
            console.error("Ignoring already published error", platform, err);
          } else {
            // throw err
          }
        } finally {
          publishSema.release();
        }
      })
    );

    // Update optional dependencies versions
    //   let nextPkg = JSON.parse(
    //     await readFile(path.join(cwd, "packages/next/package.json"))
    //   );
    //   for (let platform of platforms) {
    //     let optionalDependencies = nextPkg.optionalDependencies || {};
    //     optionalDependencies["@next/swc-" + platform] = version;
    //     nextPkg.optionalDependencies = optionalDependencies;
    //   }
    //   await writeFile(
    //     path.join(path.join(cwd, "packages/next/package.json")),
    //     JSON.stringify(nextPkg, null, 2)
    //   );
  } catch (err) {
    console.error(err);
    process.exit(1);
  }
})();
