// Non-interactive TWA build: generate project + gradle assemble + zipalign + sign
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { TwaManifest, TwaGenerator, GradleWrapper, AndroidSdkTools, JdkHelper, Config, ConsoleLog } = require('@bubblewrap/core');

const APP_DIR = process.env.APP_DIR || process.cwd();
const SDK = process.env.ANDROID_HOME;
const STORE_PW = process.env.BUBBLEWRAP_KEYSTORE_PASSWORD;
const KEY_PW = process.env.BUBBLEWRAP_KEY_PASSWORD;
const KEY_PATH = process.env.SIGNING_KEY_PATH;
const KEY_ALIAS = process.env.SIGNING_KEY_ALIAS || 'agentmail';

function sh(cmd, args, opts) {
  const { status } = require('child_process').spawnSync(cmd, args, Object.assign({ stdio: 'inherit' }, opts));
  if (status !== 0) throw new Error(cmd + ' failed with ' + status);
}

(async () => {
  const log = new ConsoleLog('twa-build');
  const manifestFile = path.join(APP_DIR, 'twa-manifest.json');
  const twaManifest = await TwaManifest.fromFile(manifestFile);

  // 1. generate android project if missing or manifest changed
  const checksum = crypto.createHash('sha1').update(fs.readFileSync(manifestFile)).digest('hex');
  const checksumFile = path.join(APP_DIR, 'manifest-checksum.txt');
  const gradleFile = path.join(APP_DIR, 'app', 'build.gradle');
  if (!fs.existsSync(gradleFile) || fs.readFileSync(checksumFile, 'utf8') !== checksum) {
    const gen = new TwaGenerator();
    await gen.createTwaProject(APP_DIR, twaManifest, log);
    fs.writeFileSync(checksumFile, checksum);
    console.log('[gen] project generated');
  } else {
    console.log('[gen] project up-to-date');
  }

  // 2. gradle assembleRelease (uses wrapper in APP_DIR)
  const jdkHelper = new JdkHelper(process, { jdkPath: process.env.JAVA_HOME, androidSdkPath: process.env.ANDROID_HOME });
  const gradleWrapper = new GradleWrapper(process, jdkHelper, APP_DIR);
  await gradleWrapper.assembleRelease();
  console.log('[gradle] assembleRelease done');

  // 3. zipalign + apksigner
  const androidSdkTools = new AndroidSdkTools(process, { jdkPath: process.env.JAVA_HOME, androidSdkPath: process.env.ANDROID_HOME }, jdkHelper);
  const APK_OUT = './app/build/outputs/apk/release/app-release-unsigned.apk';
  const ALIGNED = './app-release-unsigned-aligned.apk';
  const SIGNED = './app-release-signed.apk';
  await androidSdkTools.zipalignOnlyVerification(APK_OUT);
  fs.copyFileSync(path.join(APP_DIR, APK_OUT), path.join(APP_DIR, ALIGNED));
  await androidSdkTools.apksigner(KEY_PATH, STORE_PW, KEY_ALIAS, STORE_PW, ALIGNED, SIGNED);
  console.log('[sign] done ->', SIGNED);
})().catch(e => { console.error('BUILD-FAIL', e); process.exit(1); });
