Rails.application.routes.draw do
  # hooks.<base> answers only the webhook and the ping; everything else there
  # is an empty 404, before any other route.
  constraints(HooksHost) do
    get "ping", to: "pings#show"
    post ":name", to: "webhooks#create", constraints: { name: /[a-z][a-z0-9-]*/ }
    match "/", to: "webhooks#not_found", via: :all
    match "*path", to: "webhooks#not_found", via: :all, format: false
  end

  resource :session, only: %i[ new create destroy ]
  resource :setup, only: %i[ show create ], controller: "setup"
  namespace :setup do
    resource :cloudflare, only: %i[ show create ], controller: "cloudflare"
    resource :storage, only: %i[ show create ], controller: "storage" do
      post :finish
      get :password
    end
  end

  # Reveal health status on /up that returns 200 if the app boots with no exceptions, otherwise 500.
  # Can be used by load balancers and uptime monitors to verify that the app is live.
  get "up" => "rails/health#show", as: :rails_health_check
  get "ping" => "pings#show"

  namespace :api do
    post "projects/sync", to: "projects#sync"
    get "projects/:name/secrets/:key", to: "secrets#show", constraints: { key: %r{[^/]+} }
    post "projects/:name/deploys", to: "deploys#create"
    patch "deploys/:id", to: "deploys#update"
    post "runner/jobs/claim", to: "runner_jobs#claim"

    namespace :v1 do
      get "me", to: "me#show"
      resources :links, only: :create do
        member do
          post :access
          post :read
          post :save
        end
      end
      resources :projects, only: %i[ index show ], param: :name do
        resources :deploys, only: %i[ index show create ], param: :number
        resource :logs, only: :show
        resource :webhook, only: :show do
          post :rotate
        end
        resources :secrets, only: %i[ index update destroy ], param: :key, constraints: { key: %r{[^/]+} } do
          post :generate, on: :member
        end
      end
    end
  end

  # Add project (at /link, so no project name can shadow it).
  get "link", to: "project_links#new", as: :link
  post "link/access", to: "project_links#access", as: :link_access
  post "link/read", to: "project_links#read", as: :link_read
  post "link", to: "project_links#create"

  resources :projects, only: :show, param: :name do
    member do
      post :check
      post :rotate_webhook
    end
    resources :deploys, only: :show, param: :number
    resources :secrets, only: %i[ update destroy ], param: :key do
      post :generate, on: :member
    end
  end

  namespace :settings do
    resources :tokens, only: %i[ index create destroy ]
  end

  root "projects#index"
end
