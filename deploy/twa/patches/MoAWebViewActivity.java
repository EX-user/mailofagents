package online.mailofagents.twa;

import android.annotation.SuppressLint;
import android.app.Activity;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.content.pm.ActivityInfo;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.util.Log;
import android.view.KeyEvent;
import android.view.ViewGroup;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.ConsoleMessage;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;

/**
 * The TWA fallback shell (always used — LauncherActivity.getFallbackStrategy()
 * routes here unconditionally, Chrome-or-not). v0.6.33 formalized state:
 *  - file chooser bridge (attachments)
 *  - blob-download bridge to the system Downloads collection
 *  - MIUI fixes: force-dark off, edge-to-edge inset padding
 *  - console/error logging to logcat (tag MoA-Web) + remote debugging
 *  - notification polling lives in PollService (native foreground service);
 *    this activity only hands it the account token once the page is loaded.
 */
public class MoAWebViewActivity extends Activity {

    private static final String TAG = "MoA-Web";
    private static final int FILE_CHOOSER_REQUEST = 1002;

    private WebView mWebView;
    private ValueCallback<Uri[]> mFileCallback;

    @SuppressLint("SetJavaScriptEnabled")
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        // Setting an orientation crashes the app due to the transparent
        // background on Android 8.0 Oreo and below; UNSPECIFIED is safe on
        // every level. See https://github.com/GoogleChromeLabs/bubblewrap/issues/496.
        if (Build.VERSION.SDK_INT > Build.VERSION_CODES.O) {
            setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED);
        }
        Uri url = getIntent().getData();
        if (url == null) {
            url = Uri.parse("https://mailofagents.online/");
        }
        if (!"https".equals(url.getScheme())) {
            throw new IllegalArgumentException("launchUrl scheme must be https");
        }

        mWebView = new WebView(this);
        mWebView.setWebViewClient(new WebViewClient() {
            @Override
            public void onReceivedError(WebView view, WebResourceRequest request,
                                        WebResourceError error) {
                Log.w(TAG, "receivedError " + request.getUrl() + " " + error.getDescription());
            }
            @Override
            public void onPageFinished(WebView view, String u) {
                Log.i(TAG, "pageFinished " + u + " title=" + view.getTitle());
                injectDownloadBridge(view);
                handTokenToPollService(view);
            }
        });
        mWebView.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback,
                                             FileChooserParams params) {
                if (mFileCallback != null) {
                    mFileCallback.onReceiveValue(null);
                }
                mFileCallback = callback;
                try {
                    Intent intent = params.createIntent();
                    intent.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
                    startActivityForResult(intent, FILE_CHOOSER_REQUEST);
                    return true;
                } catch (ActivityNotFoundException e) {
                    mFileCallback = null;
                    return false;
                }
            }

            @Override
            public boolean onConsoleMessage(ConsoleMessage m) {
                Log.i(TAG, m.messageLevel() + " " + m.message()
                        + " @" + m.sourceId() + ":" + m.lineNumber());
                return true;
            }
        });

        WebSettings s = mWebView.getSettings();
        // Same capabilities the stock fallback grants (JS, DOM storage,
        // database) — login state lives in localStorage, dropping these
        // would log everyone out on the fallback path.
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setDatabaseEnabled(true);
        s.setMediaPlaybackRequiresUserGesture(true);
        s.setAllowFileAccess(false);
        // MIUI dark mode auto-inverts WebView content (force dark); the
        // algorithm mis-inverts plain text while leaving bordered buttons
        // intact. The app has its own light/dark theme, so system-forced
        // inversion is wrong twice over. (0.6.30d header fix.)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            s.setForceDark(WebSettings.FORCE_DARK_OFF);
        }
        // On-device diagnosis: chrome://inspect shows the real DOM/console.
        WebView.setWebContentsDebuggingEnabled(true);
        mWebView.addJavascriptInterface(new DlBridge(), "Android");

        // Android 15 enforces edge-to-edge for targetSdk 35: without inset
        // handling the WebView draws under the status bar and the header's
        // first line is painted behind it (0.6.30d header fix).
        android.widget.FrameLayout root = new android.widget.FrameLayout(this);
        root.setFitsSystemWindows(true);
        root.addView(mWebView, new android.widget.FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        setContentView(root);

        if (savedInstanceState != null) {
            mWebView.restoreState(savedInstanceState);
        } else {
            mWebView.loadUrl(url.toString());
        }
    }

    /**
     * Attachment downloads in the panel are blob-URL + <a download>.click()
     * (manage.js); Android WebView never acts on those, so the download
     * button silently did nothing (superior 0.6.30 feedback). Hook the
     * capture-phase click, ship the blob to the native bridge as base64 and
     * save it into the public Downloads collection there.
     */
    private void injectDownloadBridge(WebView view) {
        view.evaluateJavascript(
                "javascript:(function(){"
                + "if(window.__moaDl)return;window.__moaDl=1;"
                + "function b64(blob){return new Promise(function(res){"
                + "var r=new FileReader();r.onload=function(){"
                + "res(String(r.result).split(',')[1])};r.readAsDataURL(blob)})}"
                + "document.addEventListener('click',function(e){"
                + "var t=e.target;var a=t&&t.closest?t.closest('a[download]'):null;"
                + "if(!a)return;var href=a.getAttribute('href')||'';"
                + "if(href.indexOf('blob:')!==0)return;"
                + "e.preventDefault();e.stopPropagation();"
                + "fetch(href).then(function(r){return r.blob()})"
                + ".then(function(b){return b64(b).then(function(s){"
                + "Android.saveFile(a.getAttribute('download')||'attachment',s)})})"
                + ".catch(function(err){console.error('moa-dl-fail: '+err)});"
                + "},true);"
                + "})()", null);
    }

    /**
     * v0.6.33: notification polling lives in PollService (native foreground
     * service — WebView timers freeze in background, so the JS-side poll was
     * removed). The service needs the account's Bearer token, which lives in
     * the page's localStorage; hand it over once the page has loaded.
     */
    private void handTokenToPollService(WebView view) {
        view.evaluateJavascript(
                "(function(){try{return localStorage.getItem('agentmail_token')||''}catch(e){return ''}})()",
                new ValueCallback<String>() {
                    @Override
                    public void onReceiveValue(String raw) {
                        if (raw == null || raw.length() < 4) return;
                        String v = raw;
                        if (v.startsWith("\"")) v = v.substring(1, v.length() - 1);
                        v = v.replace("\\\"", "\"").replace("\\\\", "\\");
                        try {
                            String token = new org.json.JSONObject(v).optString("token", "");
                            if (token.isEmpty()) return;
                            Intent i = new Intent(MoAWebViewActivity.this, PollService.class);
                            i.putExtra("token", token);
                            startService(i);
                            Log.i(TAG, "poll service started");
                        } catch (Exception e) {
                            Log.w(TAG, "token parse failed: " + e);
                        }
                    }
                });
    }

    private class DlBridge {
        @android.webkit.JavascriptInterface
        public void saveFile(String name, String b64) {
            try {
                byte[] data = android.util.Base64.decode(b64, android.util.Base64.DEFAULT);
                String saved;
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                    android.content.ContentValues cv = new android.content.ContentValues();
                    cv.put(android.provider.MediaStore.Downloads.DISPLAY_NAME, name);
                    cv.put(android.provider.MediaStore.Downloads.MIME_TYPE, guessMime(name));
                    cv.put(android.provider.MediaStore.Downloads.IS_PENDING, 1);
                    Uri uri = getContentResolver().insert(
                            android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, cv);
                    java.io.OutputStream os = getContentResolver().openOutputStream(uri);
                    os.write(data);
                    os.close();
                    cv.clear();
                    cv.put(android.provider.MediaStore.Downloads.IS_PENDING, 0);
                    getContentResolver().update(uri, cv, null, null);
                    saved = "下载";
                } else {
                    java.io.File dir = getExternalFilesDir(android.os.Environment.DIRECTORY_DOWNLOADS);
                    java.io.File f = new java.io.File(dir, name);
                    java.io.FileOutputStream fo = new java.io.FileOutputStream(f);
                    fo.write(data);
                    fo.close();
                    saved = f.getAbsolutePath();
                }
                toast("已保存: " + name + " → " + saved);
            } catch (Exception e) {
                Log.w(TAG, "saveFile failed: " + e);
                toast("保存失败: " + e.getMessage());
            }
        }
    }

    private String guessMime(String name) {
        String ext = name.contains(".")
                ? name.substring(name.lastIndexOf('.') + 1).toLowerCase() : "";
        String mime = android.webkit.MimeTypeMap.getSingleton()
                .getMimeTypeFromExtension(ext);
        return mime != null ? mime : "application/octet-stream";
    }

    private void toast(final String msg) {
        runOnUiThread(new Runnable() {
            public void run() {
                android.widget.Toast.makeText(MoAWebViewActivity.this, msg,
                        android.widget.Toast.LENGTH_LONG).show();
            }
        });
    }

    @Override
    protected void onSaveInstanceState(Bundle outState) {
        super.onSaveInstanceState(outState);
        mWebView.saveState(outState);
    }

    // Deliberately NO mWebView.onPause()/onResume() overrides: pausing the
    // WebView stops its JS timers, which would freeze page-driven features.
    // PollService keeps notifications working in the background.

    @Override
    public boolean onKeyDown(int keyCode, KeyEvent event) {
        if (keyCode == KeyEvent.KEYCODE_BACK && mWebView.canGoBack()) {
            mWebView.goBack();
            return true;
        }
        return super.onKeyDown(keyCode, event);
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        if (requestCode == FILE_CHOOSER_REQUEST) {
            ValueCallback<Uri[]> callback = mFileCallback;
            mFileCallback = null;
            Uri[] value = null;
            if (resultCode == RESULT_OK && data != null && data.getData() != null) {
                value = new Uri[]{data.getData()};
            }
            if (callback != null) {
                callback.onReceiveValue(value);
            }
            return;
        }
        super.onActivityResult(requestCode, resultCode, data);
    }
}
