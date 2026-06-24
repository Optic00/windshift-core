import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '../..');

const outputPath = path.join(repoRoot, 'frontend/src/lib/generated/license-manifest.json');

const npmProjects = [
  {
    label: 'App frontend',
    directory: path.join(repoRoot, 'frontend'),
    lockfile: path.join(repoRoot, 'frontend/package-lock.json'),
  },
  {
    label: 'React demo plugin',
    directory: path.join(repoRoot, 'plugin-src/react-demo/frontend'),
    lockfile: path.join(repoRoot, 'plugin-src/react-demo/frontend/package-lock.json'),
  },
];

const LICENSE_FILE_PATTERN = /^(licen[cs]e|copying|notice|copyright)([-_.].*)?$/i;

function readJson(filePath) {
  return JSON.parse(readFileSync(filePath, 'utf8'));
}

function normalizeLicense(value) {
  if (!value) return 'Unknown';
  if (typeof value === 'string') return value.trim() || 'Unknown';
  if (Array.isArray(value)) {
    return (
      value
        .map(normalizeLicense)
        .filter((entry) => entry !== 'Unknown')
        .join(' OR ') || 'Unknown'
    );
  }
  if (typeof value === 'object') {
    if (value.type) return normalizeLicense(value.type);
    if (value.name) return normalizeLicense(value.name);
  }
  return 'Unknown';
}

function packageNameFromLockPath(lockPath) {
  const parts = lockPath.split('/');
  const index = parts.lastIndexOf('node_modules');
  if (index === -1 || index + 1 >= parts.length) return null;
  const first = parts[index + 1];
  if (first.startsWith('@') && index + 2 < parts.length) {
    return `${first}/${parts[index + 2]}`;
  }
  return first;
}

function findLicenseFiles(directory) {
  if (!directory || !existsSync(directory)) return [];

  return readdirSync(directory)
    .filter((fileName) => LICENSE_FILE_PATTERN.test(fileName))
    .map((fileName) => path.join(directory, fileName))
    .filter((filePath) => statSync(filePath).isFile())
    .sort((a, b) => path.basename(a).localeCompare(path.basename(b)));
}

function detectLicenseFromText(text) {
  const normalized = text.slice(0, 20000).replace(/\s+/g, ' ').toLowerCase();

  if (normalized.includes('apache license') && normalized.includes('version 2.0'))
    return 'Apache-2.0';
  if (
    normalized.includes('mit license') ||
    normalized.includes('permission is hereby granted, free of charge')
  )
    return 'MIT';
  if (normalized.includes('mozilla public license') && normalized.includes('version 2.0'))
    return 'MPL-2.0';
  if (normalized.includes('gnu affero general public license')) return 'AGPL-3.0';
  if (normalized.includes('gnu lesser general public license')) return 'LGPL';
  if (normalized.includes('gnu general public license')) return 'GPL';
  if (
    normalized.includes('the unlicense') ||
    normalized.includes('free and unencumbered software released into the public domain') ||
    normalized.includes('unlicense.org')
  )
    return 'Unlicense';
  if (
    normalized.includes('isc license') ||
    normalized.includes('permission to use, copy, modify, and/or distribute this software')
  )
    return 'ISC';
  if (normalized.includes('redistribution and use in source and binary forms')) {
    return normalized.includes('neither the name') ? 'BSD-3-Clause' : 'BSD-2-Clause';
  }
  if (normalized.includes('zlib license')) return 'Zlib';
  if (normalized.includes('creative commons attribution')) return 'CC-BY';

  return 'Unknown';
}

function licenseFromFiles(filePaths) {
  if (filePaths.length === 0) return { license: 'Unknown', licenseFile: null };

  const licenses = [
    ...new Set(filePaths.map((filePath) => detectLicenseFromText(readFileSync(filePath, 'utf8')))),
  ].filter((license) => license !== 'Unknown');
  const licenseFiles = filePaths.map((filePath) => {
    const relativePath = path.relative(repoRoot, filePath);
    return relativePath.startsWith('..') ? path.basename(filePath) : relativePath;
  });
  return {
    license: licenses.length > 0 ? licenses.sort().join(' OR ') : 'Unknown',
    licenseFile: licenseFiles.join(', '),
  };
}

function readNpmPackageJson(projectDirectory, lockPath) {
  const packageJsonPath = path.join(projectDirectory, lockPath, 'package.json');
  if (!existsSync(packageJsonPath)) return null;
  return readJson(packageJsonPath);
}

function collectNpmDependencies(project) {
  if (!existsSync(project.lockfile)) return [];

  const packageJson = readJson(path.join(project.directory, 'package.json'));
  const packageLock = readJson(project.lockfile);
  const directRuntime = new Set(Object.keys(packageJson.dependencies || {}));
  const directDev = new Set(Object.keys(packageJson.devDependencies || {}));
  const entriesByKey = new Map();

  for (const [lockPath, entry] of Object.entries(packageLock.packages || {})) {
    if (!lockPath.startsWith('node_modules/')) continue;

    const name = packageNameFromLockPath(lockPath);
    if (!name || !entry.version) continue;

    const installedPackage = readNpmPackageJson(project.directory, lockPath);
    const licenseFiles = findLicenseFiles(path.join(project.directory, lockPath));
    const licenseDetails = licenseFromFiles(licenseFiles);
    const license = normalizeLicense(
      entry.license ||
        installedPackage?.license ||
        installedPackage?.licenses ||
        licenseDetails.license
    );
    const scope = directRuntime.has(name)
      ? 'runtime'
      : directDev.has(name)
        ? 'development'
        : 'transitive';
    const key = `npm:${project.label}:${name}@${entry.version}`;
    const existing = entriesByKey.get(key);

    if (existing) {
      if (existing.scope === 'transitive' && scope !== 'transitive') existing.scope = scope;
      if (existing.license === 'Unknown' && license !== 'Unknown') existing.license = license;
      if (!existing.licenseFile && licenseDetails.licenseFile) {
        existing.licenseFile = licenseDetails.licenseFile;
      }
      continue;
    }

    entriesByKey.set(key, {
      ecosystem: 'npm',
      project: project.label,
      name,
      version: entry.version,
      license,
      scope,
      licenseFile: licenseDetails.licenseFile,
      homepage: installedPackage?.homepage || null,
      repository:
        typeof installedPackage?.repository === 'string'
          ? installedPackage.repository
          : installedPackage?.repository?.url || null,
    });
  }

  return [...entriesByKey.values()];
}

function parseGoListOutput(output) {
  const modules = [];
  const decoder = new TextDecoder();
  const bytes = new TextEncoder().encode(output);
  let depth = 0;
  let start = -1;

  for (let i = 0; i < bytes.length; i += 1) {
    const char = String.fromCharCode(bytes[i]);
    if (char === '{') {
      if (depth === 0) start = i;
      depth += 1;
    } else if (char === '}') {
      depth -= 1;
      if (depth === 0 && start !== -1) {
        modules.push(JSON.parse(decoder.decode(bytes.slice(start, i + 1))));
        start = -1;
      }
    }
  }

  return modules;
}

function collectGoDependencies() {
  const output = execFileSync('go', ['list', '-m', '-json', 'all'], {
    cwd: repoRoot,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
  });
  const downloadDirs = collectGoModuleDownloadDirs();

  return parseGoListOutput(output)
    .filter((module) => !module.Main)
    .map((module) => {
      const moduleDir = module.Dir || downloadDirs.get(`${module.Path}@${module.Version}`);
      const licenseDetails = licenseFromFiles(findLicenseFiles(moduleDir));

      return {
        ecosystem: 'go',
        project: 'Go module',
        name: module.Path,
        version: module.Version || 'unknown',
        license: licenseDetails.license,
        scope: module.Indirect ? 'transitive' : 'runtime',
        licenseFile: licenseDetails.licenseFile,
        homepage: `https://pkg.go.dev/${module.Path}`,
        repository: null,
      };
    })
    .sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version));
}

function collectGoModuleDownloadDirs() {
  const output = execFileSync('go', ['mod', 'download', '-json', 'all'], {
    cwd: repoRoot,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
  });

  return new Map(
    parseGoListOutput(output)
      .filter((module) => module.Dir && module.Path && module.Version)
      .map((module) => [`${module.Path}@${module.Version}`, module.Dir])
  );
}

const dependencies = [
  ...collectGoDependencies(),
  ...npmProjects.flatMap(collectNpmDependencies),
].sort((a, b) => {
  const ecosystem = a.ecosystem.localeCompare(b.ecosystem);
  if (ecosystem !== 0) return ecosystem;
  const project = a.project.localeCompare(b.project);
  if (project !== 0) return project;
  return a.name.localeCompare(b.name) || a.version.localeCompare(b.version);
});

const manifest = {
  schemaVersion: 1,
  generatedFrom: [
    'go list -m -json all',
    'frontend/package-lock.json',
    'plugin-src/react-demo/frontend/package-lock.json',
  ],
  dependencies,
};

process.stdout.write(
  `Writing ${dependencies.length} dependency license entries to ${path.relative(repoRoot, outputPath)}\n`
);
process.stdout.write(
  `${dependencies.filter((entry) => entry.license === 'Unknown').length} entries have unknown licenses\n`
);
mkdirSync(path.dirname(outputPath), { recursive: true });
writeFileSync(outputPath, `${JSON.stringify(manifest, null, 2)}\n`);
