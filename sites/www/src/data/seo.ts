/**
 * Structured data (schema.org JSON-LD) for the home page. BaseLayout adds the
 * Organization on every page. Only facts the page states: no ratings, reviews,
 * prices or usage numbers.
 */
import { release } from './release';

const origin = 'https://v8s.io';

export const softwareApplication = {
  '@type': 'SoftwareApplication',
  '@id': `${origin}/#software`,
  name: 'v8s',
  url: `${origin}/`,
  applicationCategory: 'DeveloperApplication',
  applicationSubCategory: 'Kubernetes manager',
  operatingSystem: 'macOS (Apple silicon and Intel), Windows x64, Linux x64; CLI for Linux and macOS (amd64, arm64) and Windows (amd64)',
  description:
    'Visual k8s: a fast, multi-cluster Kubernetes manager for desktop, browser and phone. A Go agent keeps compact projections of your clusters and sends the UI only the rows on screen. Problems view, Flux, Helm, Kyverno, Trivy Operator, Kubescape and Hubble.',
  license: 'https://opensource.org/license/mit',
  downloadUrl: 'https://github.com/zyno-io/v8s/releases',
  softwareVersion: release.version,
  isAccessibleForFree: true,
  publisher: { '@id': `${origin}/#organization` },
  sameAs: ['https://github.com/zyno-io/v8s'],
};

export const webSite = {
  '@type': 'WebSite',
  '@id': `${origin}/#website`,
  name: 'v8s',
  url: `${origin}/`,
  publisher: { '@id': `${origin}/#organization` },
};
