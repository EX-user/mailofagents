package online.mailofagents.twa;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Intent;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.util.Log;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;

/**
 * v0.6.32-proto8 (superior constraint 01M13CKV: backgrounded delivery is
 * enough). The JS-injected poll died with WebView timer freezing, so the
 * poll moved here: a foreground service (survives backgrounding; MIUI
 * respects it) that checks /api/inbox every 20s with the account's Bearer
 * token and posts a heads-up notification on unread increase. Its ongoing
 * "heartbeat" notification doubles as the freeze detector — if the time
 * stops advancing, the platform killed us.
 */
public class PollService extends Service {

    private static final String TAG = "MoA-Poll";
    private static final String BASE = "https://mailofagents.online";
    private static final long INTERVAL_MS = 2000;

    private final Handler mHandler = new Handler(Looper.getMainLooper());
    private String mToken;
    private int mLastUnread = -1;
    private boolean mRunning;
    // v0.1 (new repo train): default 2s per the superior's field feedback
    // (01M14E9DT: red dot beats the ring by ~1min under the 60s default).
    // Still configurable via intent extra "interval_ms".
    private long mIntervalMs = INTERVAL_MS;

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null) {
            mToken = intent.getStringExtra("token");
            // Interval is configurable; the shipped default is 2s (superior's
            // explicit request, 01M14E9DT). Raise it via "interval_ms" only
            // with his say-so — he already felt the 60s lag.
            mIntervalMs = intent.getLongExtra("interval_ms", mIntervalMs);
        }
        // proto12 cleanup: the prototype iterations left three stale "新信通知"
        // channels (moa/moa-high/moa-high2 — channel settings are immutable,
        // so each fix had to switch ids). Delete them; only moa-high3 stays.
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        nm.deleteNotificationChannel("moa");
        nm.deleteNotificationChannel("moa-high");
        nm.deleteNotificationChannel("moa-high2");
        startForeground(2001, buildHeartbeat("查信服务启动"));
        if (!mRunning) {
            mRunning = true;
            mHandler.postDelayed(mTick, 3000);
        }
        return START_STICKY;
    }

    @Override
    public void onTaskRemoved(Intent rootIntent) {
        // User swiped the app away: stop the poll and clear the foreground
        // notification so no zombie heartbeat lingers for a dead task.
        mRunning = false;
        mHandler.removeCallbacksAndMessages(null);
        stopForeground(true);
        stopSelf();
        super.onTaskRemoved(rootIntent);
    }

    private final Runnable mTick = new Runnable() {
        @Override
        public void run() {
            new Thread(new Runnable() {
                @Override
                public void run() {
                    pollOnce();
                    mHandler.postDelayed(mTick, mIntervalMs);
                }
            }).start();
        }
    };

    private void pollOnce() {
        if (mToken == null || mToken.isEmpty()) return;
        HttpURLConnection c = null;
        try {
            c = (HttpURLConnection) new URL(BASE + "/api/inbox?limit=1").openConnection();
            c.setRequestProperty("Authorization", "Bearer " + mToken);
            c.setConnectTimeout(10000);
            c.setReadTimeout(10000);
            BufferedReader r = new BufferedReader(new InputStreamReader(c.getInputStream()));
            StringBuilder sb = new StringBuilder();
            String line;
            while ((line = r.readLine()) != null) sb.append(line);
            r.close();
            JSONObject d = new JSONObject(sb.toString());
            int n = d.optInt("unread_count", 0);
            String from = "";
            org.json.JSONArray letters = d.optJSONArray("letters");
            if (letters != null && letters.length() > 0) {
                from = letters.getJSONObject(0).optString("from", "");
            }
            Log.i(TAG, "poll unread=" + n);
            if (mLastUnread != -1 && n > mLastUnread) {
                postAlert(n, from);
            }
            mLastUnread = n;
            updateHeartbeat(n);
        } catch (Exception e) {
            Log.w(TAG, "poll failed: " + e);
        } finally {
            if (c != null) c.disconnect();
        }
    }

    private void postAlert(int n, String from) {
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26) {
            // Channel sound/vibration MUST be set BEFORE createNotificationChannel —
            // setters after creation are silently ignored (proto10 bug: the
            // channel was created first, then configured, so it stayed silent).
            NotificationChannel hi = new NotificationChannel("moa-high3",
                    "新信通知", NotificationManager.IMPORTANCE_HIGH);
            hi.enableVibration(true);
            hi.setVibrationPattern(new long[]{0, 300, 200, 300});
            hi.setSound(android.provider.Settings.System.DEFAULT_NOTIFICATION_URI,
                    new android.media.AudioAttributes.Builder()
                            .setUsage(android.media.AudioAttributes.USAGE_NOTIFICATION)
                            .setContentType(android.media.AudioAttributes.CONTENT_TYPE_SONIFICATION)
                            .build());
            nm.createNotificationChannel(hi);
        }
        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, "moa-high3")
                : new Notification.Builder(this);
        b.setSmallIcon(android.R.drawable.ic_dialog_email)
                .setContentTitle("Mail of Agents")
                .setContentText("新信 " + n + " 封" + (from.isEmpty() ? "" : " · " + from))
                .setAutoCancel(true);
        if (Build.VERSION.SDK_INT < 26) {
            b.setDefaults(Notification.DEFAULT_SOUND | Notification.DEFAULT_VIBRATE);
        }
        nm.notify(1001, b.build());
    }

    private Notification buildHeartbeat(String text) {
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(new NotificationChannel("moa-poll",
                    "查信状态（常驻）", NotificationManager.IMPORTANCE_MIN));
        }
        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, "moa-poll")
                : new Notification.Builder(this);
        b.setSmallIcon(android.R.drawable.ic_dialog_email)
                .setContentTitle("Mail of Agents 查信中")
                .setContentText(text)
                .setOngoing(true);
        return b.build();
    }

    private void updateHeartbeat(int unread) {
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        String time = new SimpleDateFormat("HH:mm:ss", Locale.getDefault()).format(new Date());
        Notification n = buildHeartbeat("上次查信 " + time + " · 未读 " + unread);
        nm.notify(2001, n);
    }

    @Override
    public void onDestroy() {
        mRunning = false;
        mHandler.removeCallbacksAndMessages(null);
        super.onDestroy();
    }
}
